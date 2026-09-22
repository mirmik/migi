package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class VideoQueuePolicyTest {
    private val video = PlaybackTrack("ab".repeat(16), "Episode 01", "", "video/x-matroska", 3L shl 30, "cd".repeat(32))
    private fun queue(vararg tracks: PlaybackTrack) = PlaybackQueue(12, "Season 1", "agent", "phone-1", tracks.toList())
    @Test fun acceptsSeasonWithoutDownloadingOrApplyingAudioByteLimits() {
        val season = queue(*Array(24) { video })
        assertEquals(season, PlaybackQueueCodec.validate(season, video = true))
    }
    @Test fun rejectsVideoInMusicAndAudioInVideoQueue() {
        assertThrows(IllegalArgumentException::class.java) { PlaybackQueueCodec.validate(queue(video)) }
        assertThrows(IllegalArgumentException::class.java) {
            PlaybackQueueCodec.validate(queue(video, video.copy(mime = "audio/mp4", size = 1024)), video = true)
        }
    }
    @Test fun rejectsOversizedVideoAndInvalidIdentifiers() {
        for (track in listOf(video.copy(size = PlaybackQueueCodec.MAX_VIDEO_BYTES + 1), video.copy(id = "../movie"), video.copy(sha256 = "bad"))) {
            assertThrows(IllegalArgumentException::class.java) { PlaybackQueueCodec.validate(queue(track), video = true) }
        }
    }
}
