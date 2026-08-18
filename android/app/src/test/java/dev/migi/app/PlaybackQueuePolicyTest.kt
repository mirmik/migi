package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class PlaybackQueuePolicyTest {
	private val track = PlaybackTrack(
		id = "0123456789abcdef0123456789abcdef",
		title = "A quiet track",
		artist = "Migi",
		mime = "audio/opus",
		size = 1_024,
		sha256 = "ab".repeat(32),
	)

	@Test
	fun acceptsBoundedAudioQueue() {
		val queue = PlaybackQueue(
			eventID = 42,
			name = "Focus",
			agent = "playlist-agent",
			deviceID = "phone-1",
			items = listOf(track),
		)
		assertEquals(queue, PlaybackQueueCodec.validate(queue))
	}

	@Test
	fun rejectsArbitraryMediaIdentifiers() {
		val queue = PlaybackQueue(
			eventID = 42,
			name = "Focus",
			agent = "playlist-agent",
			deviceID = "",
			items = listOf(track.copy(id = "../../shared-file")),
		)
		assertThrows(IllegalArgumentException::class.java) {
			PlaybackQueueCodec.validate(queue)
		}
	}

	@Test
	fun rejectsNonAudioAndOversizedQueues() {
		val nonAudio = PlaybackQueue(
			eventID = 42,
			name = "Focus",
			agent = "playlist-agent",
			deviceID = "",
			items = listOf(track.copy(mime = "text/html")),
		)
		assertThrows(IllegalArgumentException::class.java) {
			PlaybackQueueCodec.validate(nonAudio)
		}

		val tooLarge = nonAudio.copy(
			items = List(5) { track.copy(size = PlaybackQueueCodec.MAX_TRACK_BYTES) },
		)
		assertThrows(IllegalArgumentException::class.java) {
			PlaybackQueueCodec.validate(tooLarge)
		}
	}
}
