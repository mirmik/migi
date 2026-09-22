package dev.migi.app

import android.content.Context
import java.io.File
import java.io.InputStream
import org.json.JSONArray
import org.json.JSONObject

internal data class VideoEntry(val track: PlaybackTrack, val collection: String)

/** Separate from music: receiving another episode never replaces an audio session. */
internal class VideoLibrary(context: Context) {
    private val app = context.applicationContext
    private val prefs = app.getSharedPreferences("video-library", Context.MODE_PRIVATE)

    fun accept(event: AgentEvent): Boolean = synchronized(LOCK) {
        val queue = runCatching { PlaybackQueueCodec.parse(event, video = true) }.getOrNull() ?: return false
        if (queue.deviceID.isNotEmpty() && queue.deviceID != DeviceIdentity.get(app)) return false
        if (event.id <= prefs.getLong("event", 0)) return false
        val manifest = JSONObject(event.body)
        val incoming = manifest.getJSONArray("items")
        val old = rawEntries()
        val ids = queue.items.map { it.id }.toSet()
        val result = JSONArray()
        for (i in 0 until incoming.length()) {
            result.put(incoming.getJSONObject(i).put("collection", queue.name))
        }
        for (i in 0 until old.length()) {
            val item = old.getJSONObject(i)
            if (item.getString("id") !in ids) result.put(item)
        }
        check(prefs.edit().putString("entries", result.toString()).putLong("event", event.id).commit())
        true
    }

    private fun rawEntries() = JSONArray(prefs.getString("entries", "[]"))
    fun entries(): List<VideoEntry> = synchronized(LOCK) {
        val items = rawEntries()
        (0 until items.length()).map { index ->
            val item = items.getJSONObject(index)
            VideoEntry(PlaybackQueueCodec.parseTrack(item), item.getString("collection"))
        }
    }
    fun position(track: PlaybackTrack) = prefs.getLong("position-${track.sha256}", 0)
    fun watched(track: PlaybackTrack) = prefs.getBoolean("watched-${track.sha256}", false)
    fun savePosition(track: PlaybackTrack, position: Long, ended: Boolean = false) {
        prefs.edit().putLong("position-${track.sha256}", if (ended) 0 else position.coerceAtLeast(0))
            .apply { if (ended) putBoolean("watched-${track.sha256}", true) }.apply()
    }
    private fun subtitleFile(track: PlaybackTrack) = File(File(app.filesDir, "video-subtitles"), track.sha256)
    fun subtitle(track: PlaybackTrack): Pair<File, String>? {
        val mime = prefs.getString("subtitle-${track.sha256}", null) ?: return null
        return subtitleFile(track).takeIf { it.isFile }?.let { it to mime }
    }
    fun installSubtitle(track: PlaybackTrack, source: InputStream, mime: String) {
        val file = subtitleFile(track)
        file.parentFile!!.mkdirs()
        VideoSubtitleFile.copy(source, file)
        prefs.edit().putString("subtitle-${track.sha256}", mime).apply()
    }
    fun removeSubtitle(track: PlaybackTrack) {
        subtitleFile(track).delete()
        prefs.edit().remove("subtitle-${track.sha256}").apply()
    }
    fun reset() = synchronized(LOCK) {
        prefs.edit().clear().commit(); VideoDownloads.clear(app)
        File(app.filesDir, "video-subtitles").deleteRecursively()
    }
    companion object { private val LOCK = Any() }
}
