package dev.migi.app

import android.content.Context
import android.os.ParcelFileDescriptor
import android.util.Log
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.Executors
import org.json.JSONObject

/** Process-owned worker survives activity recreation; .part survives process/network failure. */
internal object VideoDownloads {
    private val executor = Executors.newSingleThreadExecutor()
    @Volatile var active: String? = null; private set
    @Volatile var error: String? = null; private set
    @Volatile private var generation = 0L
    private fun directory(context: Context) = File(context.filesDir, "video-media").apply { mkdirs() }
    fun file(context: Context, track: PlaybackTrack) = File(directory(context), track.sha256 + ".video")
    fun partial(context: Context, track: PlaybackTrack) = File(directory(context), track.sha256 + ".part")
    fun available(context: Context, track: PlaybackTrack) = file(context, track).let { it.isFile && it.length() == track.size }
    fun bytes(context: Context, track: PlaybackTrack) = if (available(context, track)) track.size else partial(context, track).length()
    @Synchronized fun remove(context: Context, track: PlaybackTrack) {
        check(active != track.id) { "Дождитесь завершения загрузки" }
        check(!file(context, track).exists() || file(context, track).delete()) { "Не удалось удалить видео" }
        check(!partial(context, track).exists() || partial(context, track).delete()) { "Не удалось удалить неполную загрузку" }
        File(context.filesDir, "video-sidecars/${track.sha256}").deleteRecursively()
    }
    @Synchronized fun clear(context: Context) {
        generation++
        directory(context).listFiles()?.forEach { it.delete() }
        File(context.filesDir, "video-sidecars").deleteRecursively()
    }
    @Synchronized fun start(context: Context, track: PlaybackTrack) {
        if (active != null) return
        val app = context.applicationContext
        val expectedGeneration = generation
        val preferences = app.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
        // Capture the identity before starting so a re-pair cannot redirect this request.
        val endpoint = preferences.getString(MainActivity.KEY_ENDPOINT, "")!!
        val pin = preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, "")!!
        val credential = CredentialStore(app).load().orEmpty()
        active = track.id; error = null
        executor.execute {
            try {
                require(endpoint.startsWith("https://") && pin.isNotEmpty() && credential.isNotEmpty()) { "Подключите Migi к серверу" }
                val subtitles = PlaybackMediaCache(app, "video-sidecars/${track.sha256}", 32L shl 20)
                for (subtitle in track.subtitles) {
                    check(generation == expectedGeneration) { "Подключение изменилось" }
                    subtitles.prepare(subtitle)
                }
                val target = file(app, track)
                val part = partial(app, track)
                if (target.exists()) {
                    if (target.length() == track.size && sha256(target) == track.sha256) return@execute
                    check(target.delete())
                }
                if (part.length() > track.size || part.length() == track.size && sha256(part) != track.sha256) check(part.delete())
                check(directory(app).usableSpace > track.size - part.length() + 32L * 1024 * 1024) { "Недостаточно места на телефоне" }
                Log.i("MigiVideo", "Download ${track.id}: resume=${part.length()} total=${track.size}")
                if (part.length() < track.size) {
                    ParcelFileDescriptor.open(part, ParcelFileDescriptor.MODE_CREATE or ParcelFileDescriptor.MODE_READ_WRITE).use { descriptor ->
                        val response = NativeQuicClient.downloadMedia(endpoint, pin, credential, track.id, descriptor.fd, track.size)
                        check(!response.startsWith("MIGI_ERROR:")) { response.removePrefix("MIGI_ERROR:") }
                        val verified = JSONObject(response)
                        require(verified.getLong("bytes") == track.size && verified.getString("sha256") == track.sha256) { "Проверка видео не пройдена" }
                    }
                }
                require(part.length() == track.size && sha256(part) == track.sha256) { "Проверка видео не пройдена" }
                synchronized(this) {
                    check(generation == expectedGeneration) { "Подключение изменилось" }
                    check(part.renameTo(target)) { "Не удалось сохранить видео" }
                    Log.i("MigiVideo", "Verified ${track.id}: bytes=${target.length()} sha256=${track.sha256}")
                }
            } catch (e: Exception) {
                error = e.message ?: "Не удалось загрузить видео"
                Log.w("MigiVideo", "Download ${track.id} stopped at ${bytes(app, track)}/${track.size}: $error")
            } finally {
                active = null
            }
        }
    }
    private fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(256 * 1024)
            while (true) { val count = input.read(buffer); if (count < 0) break; digest.update(buffer, 0, count) }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }
}
