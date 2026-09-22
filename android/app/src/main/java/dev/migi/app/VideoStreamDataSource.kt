package dev.migi.app

import android.content.Context
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.media3.common.C
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.BaseDataSource
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.DataSpec
import androidx.media3.datasource.DefaultDataSource
import java.io.File
import java.io.IOException
import java.io.InterruptedIOException
import org.json.JSONObject

/** Each player owns a bounded, in-memory cache. Offline files remain independently verified. */
internal class VideoChunkCache(private val limit: Int = 16) {
    private val blocks = LinkedHashMap<Long, ByteArray>(limit, 0.75f, true)
    @Synchronized fun get(offset: Long): ByteArray? = blocks[offset]
    @Synchronized fun put(offset: Long, bytes: ByteArray) {
        require(bytes.size <= CHUNK_SIZE)
        blocks[offset] = bytes
        while (blocks.size > limit) blocks.remove(blocks.keys.first())
    }
    companion object { const val CHUNK_SIZE = 2 * 1024 * 1024 }
}

@UnstableApi
internal class VideoStreamDataSource(
    private val context: Context,
    private val track: PlaybackTrack,
    private val cache: VideoChunkCache,
) : BaseDataSource(true) {
    private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val endpoint = preferences.getString(MainActivity.KEY_ENDPOINT, "").orEmpty()
    private val pin = preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, "").orEmpty()
    private val credential = CredentialStore(context).load().orEmpty()
    private var uri: Uri? = null
    private var position = 0L
    private var remaining = 0L
    private var opened = false

    override fun open(dataSpec: DataSpec): Long {
        transferInitializing(dataSpec)
        if (dataSpec.position < 0 || dataSpec.position > track.size) throw IOException("Позиция за пределами видео")
        uri = dataSpec.uri
        position = dataSpec.position
        remaining = if (dataSpec.length == C.LENGTH_UNSET.toLong()) track.size - position else minOf(dataSpec.length, track.size - position)
        opened = true
        transferStarted(dataSpec)
        return remaining
    }

    override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
        if (length == 0) return 0
        if (remaining == 0L) return C.RESULT_END_OF_INPUT
        if (Thread.currentThread().isInterrupted) throw InterruptedIOException()
        val start = position / VideoChunkCache.CHUNK_SIZE * VideoChunkCache.CHUNK_SIZE
        val block = cache.get(start) ?: fetch(start).also { cache.put(start, it) }
        val inside = (position - start).toInt()
        val count = minOf(length.toLong(), remaining, (block.size - inside).toLong()).toInt()
        if (count <= 0) throw IOException("Неполный блок видео")
        block.copyInto(buffer, offset, inside, inside + count)
        position += count; remaining -= count
        bytesTransferred(count)
        return count
    }

    private fun fetch(start: Long): ByteArray {
        if (credential.isEmpty() || pin.isEmpty()) throw IOException("Подключите Migi к серверу")
        // A private temporary file bridges JNI; never promote a chunk into the offline library.
        val file = File.createTempFile("video-stream-", ".chunk", context.cacheDir)
        try {
            val result = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_WRITE or ParcelFileDescriptor.MODE_TRUNCATE).use {
                NativeQuicClient.downloadMediaChunk(endpoint, pin, credential, track.id, track.sha256, start, track.size, it.fd)
            }
            if (Thread.currentThread().isInterrupted) throw InterruptedIOException()
            if (result.startsWith("MIGI_ERROR:")) throw IOException(result.removePrefix("MIGI_ERROR:"))
            val expected = minOf(VideoChunkCache.CHUNK_SIZE.toLong(), track.size - start)
            if (JSONObject(result).getLong("bytes") != expected || file.length() != expected) throw IOException("Неполный блок видео")
            Log.i("MigiVideo", "Stream ${track.id}: offset=$start bytes=$expected")
            return file.readBytes()
        } catch (error: IOException) { throw error }
        catch (error: Exception) { throw IOException("Не удалось получить блок видео", error) }
        finally { file.delete() }
    }

    override fun getUri(): Uri? = uri
    override fun close() {
        uri = null
        if (opened) { opened = false; transferEnded() }
    }

    companion object {
        fun factory(context: Context, track: PlaybackTrack): DataSource.Factory {
            val app = context.applicationContext
            val cache = VideoChunkCache()
            // DefaultDataSource also handles private external subtitle files.
            return DefaultDataSource.Factory(app, DataSource.Factory { VideoStreamDataSource(app, track, cache) })
        }
    }
}
