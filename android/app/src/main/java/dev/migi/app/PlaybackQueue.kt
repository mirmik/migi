package dev.migi.app

import android.content.Context
import org.json.JSONObject

data class PlaybackTrack(
	val id: String,
	val title: String,
	val artist: String,
	val mime: String,
	val size: Long,
	val sha256: String,
	val subtitles: List<PlaybackSubtitle> = emptyList(),
)

data class PlaybackSubtitle(
    val id: String, val label: String, val language: String, val default: Boolean,
    val mime: String, val size: Long, val sha256: String,
)

data class PlaybackArtwork(
	val id: String,
	val mime: String,
	val size: Long,
	val sha256: String,
)

data class PlaybackQueue(
	val eventID: Long,
	val name: String,
	val agent: String,
	val deviceID: String,
	val items: List<PlaybackTrack>,
	val artwork: PlaybackArtwork? = null,
    val playlistID: String? = null,
)

internal object PlaybackQueueCodec {
	fun parse(event: AgentEvent, video: Boolean = false): PlaybackQueue {
		require(event.kind == if (video) VIDEO_EVENT_KIND else EVENT_KIND) { "Not a playback queue event" }
		val manifest = JSONObject(event.body)
		require(manifest.getInt("version") == SCHEMA_VERSION) { "Unsupported playback queue version" }
		val name = manifest.getString("name")
		val deviceID = manifest.optString("device_id")
		val array = manifest.getJSONArray("items")
		val items = ArrayList<PlaybackTrack>(array.length())
		for (index in 0 until array.length()) {
			items += parseTrack(array.getJSONObject(index))
		}
		return validate(PlaybackQueue(
			eventID = event.id,
			name = name,
			agent = event.agent.ifBlank { "agent" },
			deviceID = deviceID,
			items = items,
			artwork = manifest.optJSONObject("artwork")?.let(::parseArtwork),
            playlistID = if (manifest.has("playlist_id")) manifest.getString("playlist_id") else null,
		), video)
	}

	internal fun validate(queue: PlaybackQueue, video: Boolean = false): PlaybackQueue {
		require(queue.playlistID.isNullOrEmpty() || MEDIA_ID.matches(queue.playlistID)) { "Playlist ID is invalid" }
		require(queue.eventID > 0) { "Playback queue event ID is invalid" }
		require(validText(queue.name, MAX_QUEUE_NAME_LENGTH)) { "Playback queue name is invalid" }
		require(queue.deviceID.isEmpty() || DEVICE_ID.matches(queue.deviceID)) { "Playback target is invalid" }
		require(queue.items.isNotEmpty()) { "Playback queue item count is invalid" }
		var totalBytes = 0L
		for (track in queue.items) {
			require(MEDIA_ID.matches(track.id)) { "Media ID is invalid" }
			require(validText(track.title, MAX_TRACK_TEXT_LENGTH)) { "Track title is invalid" }
			require(track.artist.isEmpty() || validText(track.artist, MAX_TRACK_TEXT_LENGTH)) { "Track artist is invalid" }
			require(track.mime.startsWith(if (video) "video/" else "audio/") && track.mime.length <= 127) { "Track MIME type is invalid" }
			require(track.size in 1..(if (video) MAX_VIDEO_BYTES else MAX_TRACK_BYTES)) { "Track size is invalid" }
			require(SHA256.matches(track.sha256)) { "Track digest is invalid" }
            require(video || track.subtitles.isEmpty()) { "Audio cannot carry video subtitles" }
            validateSubtitles(track.subtitles)
			totalBytes = Math.addExact(totalBytes, track.size)
			require(totalBytes <= if (video) MAX_VIDEO_QUEUE_BYTES else MAX_QUEUE_BYTES) { "Playback queue is too large" }
		}
		queue.artwork?.let { artwork ->
			require(MEDIA_ID.matches(artwork.id)) { "Artwork media ID is invalid" }
			require(artwork.mime in ARTWORK_MIME_TYPES) { "Artwork MIME type is invalid" }
			require(artwork.size in 1..MAX_ARTWORK_BYTES) { "Artwork size is invalid" }
			require(SHA256.matches(artwork.sha256)) { "Artwork digest is invalid" }
		}
		return queue
	}

	internal fun parseTrack(json: JSONObject): PlaybackTrack {
		val id = json.getString("id")
		val title = json.getString("title")
		val artist = json.optString("artist")
		val mime = json.getString("mime").lowercase()
		val size = json.getLong("size")
		val sha256 = json.getString("sha256").lowercase()
		val array = if (json.has("subtitles")) json.getJSONArray("subtitles") else null
        require(array == null || array.length() <= 8) { "Too many subtitles" }
        val subtitles = if (array == null) emptyList() else (0 until array.length()).map { index ->
            val item = array.getJSONObject(index)
            PlaybackSubtitle(item.getString("id"), item.getString("label"), item.optString("language"),
                item.optBoolean("default"), item.getString("mime"), item.getLong("size"), item.getString("sha256"))
        }
        validateSubtitles(subtitles)
        return PlaybackTrack(id, title, artist, mime, size, sha256, subtitles)
	}

    internal fun validateSubtitles(subtitles: List<PlaybackSubtitle>) {
        require(subtitles.size <= 8 && subtitles.map { it.id }.distinct().size == subtitles.size) { "Invalid subtitle count or duplicate ID" }
        require(subtitles.count { it.default } <= 1) { "Multiple default subtitles" }
        for (subtitle in subtitles) {
            require(MEDIA_ID.matches(subtitle.id) && SHA256.matches(subtitle.sha256)) { "Invalid subtitle identity" }
            require(validText(subtitle.label, 128)) { "Invalid subtitle label" }
            require(subtitle.language.length <= 35 && (subtitle.language.isEmpty() || Regex("^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$").matches(subtitle.language))) { "Invalid subtitle language" }
            require(subtitle.mime in setOf("text/x-ssa", "application/x-subrip", "text/vtt") && subtitle.size in 1..(4L shl 20)) { "Invalid subtitle format or size" }
        }
    }

	private fun parseArtwork(json: JSONObject): PlaybackArtwork = PlaybackArtwork(
		id = json.getString("id"),
		mime = json.getString("mime").lowercase(),
		size = json.getLong("size"),
		sha256 = json.getString("sha256").lowercase(),
	)

	private fun validText(value: String, maxLength: Int): Boolean =
		value.isNotBlank() && value == value.trim() && value.length <= maxLength && value.none(Char::isISOControl)

	const val VIDEO_EVENT_KIND = "video.queue.set"
	const val MAX_VIDEO_BYTES = 8L * 1024 * 1024 * 1024
	const val MAX_VIDEO_QUEUE_BYTES = 1024L * 1024 * 1024 * 1024
	const val EVENT_KIND = "media.queue.set"
	const val SCHEMA_VERSION = 1
	const val MAX_TRACK_BYTES = 256L * 1024 * 1024
	const val MAX_QUEUE_BYTES = 1024L * 1024 * 1024
	const val MAX_ARTWORK_BYTES = 8L * 1024 * 1024
	private const val MAX_QUEUE_NAME_LENGTH = 128
	private const val MAX_TRACK_TEXT_LENGTH = 256
	private val MEDIA_ID = Regex("^[a-f0-9]{32}$")
	private val SHA256 = Regex("^[a-f0-9]{64}$")
	private val DEVICE_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
	private val ARTWORK_MIME_TYPES = setOf("image/jpeg", "image/png", "image/webp")
}

internal class PlaybackQueueRepository(private val context: Context) {
	private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)

	enum class Acceptance {
		STORED,
		DUPLICATE,
		NOT_TARGETED,
		INVALID,
	}

	fun accept(event: AgentEvent): Acceptance {
		val queue = runCatching { PlaybackQueueCodec.parse(event) }.getOrElse {
			return Acceptance.INVALID
		}
		if (queue.deviceID.isNotEmpty() && queue.deviceID != DeviceIdentity.get(context)) {
			return Acceptance.NOT_TARGETED
		}
		if (queue.eventID <= preferences.getLong(KEY_EVENT_ID, 0)) {
			return Acceptance.DUPLICATE
		}
		val envelope = JSONObject()
			.put("event_id", event.id)
			.put("agent", event.agent)
			.put("manifest", JSONObject(event.body))
		check(
			preferences.edit()
				.putString(KEY_QUEUE, envelope.toString())
				.putLong(KEY_EVENT_ID, event.id)
				.commit(),
		) { "Failed to persist playback queue" }
		return Acceptance.STORED
	}

	fun current(): PlaybackQueue? {
		val raw = preferences.getString(KEY_QUEUE, null) ?: return null
		return runCatching {
			val envelope = JSONObject(raw)
			val manifest = envelope.getJSONObject("manifest")
			PlaybackQueueCodec.parse(
				AgentEvent(
					id = envelope.getLong("event_id"),
					kind = PlaybackQueueCodec.EVENT_KIND,
					agent = envelope.optString("agent"),
					title = manifest.getString("name"),
					body = manifest.toString(),
					createdAt = java.time.Instant.EPOCH,
				),
			)
		}.getOrNull()
	}

	fun reset() {
		check(
			preferences.edit()
				.remove(KEY_QUEUE)
				.remove(KEY_EVENT_ID)
				.commit(),
		) { "Failed to clear playback queue" }
	}

	companion object {
		const val KEY_QUEUE = "playback_queue"
		const val KEY_EVENT_ID = "playback_queue_event_id"
	}
}
