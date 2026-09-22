package dev.migi.app

import android.net.Uri
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.DataSpec
import androidx.media3.datasource.FileDataSource
import androidx.media3.datasource.TransferListener
import androidx.media3.common.util.UnstableApi
import java.io.IOException

/** Resolves only subtitle IDs declared by this video, then reads verified private bytes. */
@UnstableApi
internal class VideoSubtitleDataSource(
    private val track: PlaybackTrack,
    private val cache: PlaybackMediaCache,
    private val video: DataSource,
) : DataSource {
    private val file = FileDataSource()
    private var active: DataSource = video
    override fun addTransferListener(listener: TransferListener) {
        video.addTransferListener(listener); file.addTransferListener(listener)
    }
    override fun open(dataSpec: DataSpec): Long {
        if (dataSpec.uri.scheme != "migi-subtitle") {
            active = video
            return active.open(dataSpec)
        }
        val subtitle = track.subtitles.find { it.id == dataSpec.uri.host }
            ?: throw IOException("Субтитры не относятся к этому видео")
        val local = try { cache.prepare(subtitle) }
            catch (error: Exception) { throw IOException("Не удалось загрузить субтитры: ${subtitle.label}", error) }
        active = file
        return active.open(dataSpec.buildUpon().setUri(Uri.fromFile(local)).build())
    }
    override fun read(buffer: ByteArray, offset: Int, length: Int) = active.read(buffer, offset, length)
    override fun getUri(): Uri? = active.uri
    override fun getResponseHeaders(): Map<String, List<String>> = active.responseHeaders
    override fun close() = active.close()
}
