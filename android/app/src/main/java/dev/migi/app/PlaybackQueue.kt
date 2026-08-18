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
)

internal object PlaybackQueueCodec {
	fun parse(event: AgentEvent): PlaybackQueue {
		require(event.kind == EVENT_KIND) { "Not a playback queue event" }
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
		))
	}

	internal fun validate(queue: PlaybackQueue): PlaybackQueue {
		require(queue.eventID > 0) { "Playback queue event ID is invalid" }
		require(validText(queue.name, MAX_QUEUE_NAME_LENGTH)) { "Playback queue name is invalid" }
		require(queue.deviceID.isEmpty() || DEVICE_ID.matches(queue.deviceID)) { "Playback target is invalid" }
		require(queue.items.size in 1..MAX_ITEMS) { "Playback queue item count is invalid" }
		var totalBytes = 0L
		for (track in queue.items) {
			require(MEDIA_ID.matches(track.id)) { "Media ID is invalid" }
			require(validText(track.title, MAX_TRACK_TEXT_LENGTH)) { "Track title is invalid" }
			require(track.artist.isEmpty() || validText(track.artist, MAX_TRACK_TEXT_LENGTH)) { "Track artist is invalid" }
			require(track.mime.startsWith("audio/") && track.mime.length <= 127) { "Track MIME type is invalid" }
			require(track.size in 1..MAX_TRACK_BYTES) { "Track size is invalid" }
			require(SHA256.matches(track.sha256)) { "Track digest is invalid" }
			totalBytes = Math.addExact(totalBytes, track.size)
			require(totalBytes <= MAX_QUEUE_BYTES) { "Playback queue is too large" }
		}
		queue.artwork?.let { artwork ->
			require(MEDIA_ID.matches(artwork.id)) { "Artwork media ID is invalid" }
			require(artwork.mime in ARTWORK_MIME_TYPES) { "Artwork MIME type is invalid" }
			require(artwork.size in 1..MAX_ARTWORK_BYTES) { "Artwork size is invalid" }
			require(SHA256.matches(artwork.sha256)) { "Artwork digest is invalid" }
		}
		return queue
	}

	private fun parseTrack(json: JSONObject): PlaybackTrack {
		val id = json.getString("id")
		val title = json.getString("title")
		val artist = json.optString("artist")
		val mime = json.getString("mime").lowercase()
		val size = json.getLong("size")
		val sha256 = json.getString("sha256").lowercase()
		return PlaybackTrack(id, title, artist, mime, size, sha256)
	}

	private fun parseArtwork(json: JSONObject): PlaybackArtwork = PlaybackArtwork(
		id = json.getString("id"),
		mime = json.getString("mime").lowercase(),
		size = json.getLong("size"),
		sha256 = json.getString("sha256").lowercase(),
	)

	private fun validText(value: String, maxLength: Int): Boolean =
		value.isNotBlank() && value == value.trim() && value.length <= maxLength && value.none(Char::isISOControl)

	const val EVENT_KIND = "media.queue.set"
	const val SCHEMA_VERSION = 1
	const val MAX_ITEMS = 32
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
