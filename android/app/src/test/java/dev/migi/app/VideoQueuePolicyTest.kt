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
    private val subtitle = PlaybackSubtitle("ef".repeat(16), "Русские", "ru", true,
        "application/x-subrip", 4096, "01".repeat(32))
    @Test fun acceptsMultipleVerifiedVideoSubtitles() {
        val track = video.copy(subtitles = listOf(subtitle, subtitle.copy(id = "02".repeat(16), language = "en", default = false)))
        assertEquals(track, PlaybackQueueCodec.validate(queue(track), video = true).items.single())
    }
    @Test fun rejectsInvalidSubtitleMetadataAndAudioAttachments() {
        for (sub in listOf(subtitle.copy(id = "../file"), subtitle.copy(sha256 = "bad"),
            subtitle.copy(size = (4L shl 20) + 1), subtitle.copy(size = 0),
            subtitle.copy(mime = "text/html"), subtitle.copy(language = "../ru"), subtitle.copy(label = "bad\nlabel"))) {
            assertThrows(IllegalArgumentException::class.java) {
                PlaybackQueueCodec.validate(queue(video.copy(subtitles = listOf(sub))), video = true)
            }
        }
        for (subs in listOf(List(9) { subtitle }, listOf(subtitle, subtitle),
            listOf(subtitle, subtitle.copy(id = "03".repeat(16))))) {
            assertThrows(IllegalArgumentException::class.java) { PlaybackQueueCodec.validateSubtitles(subs) }
        }
        assertThrows(IllegalArgumentException::class.java) {
            PlaybackQueueCodec.validate(queue(video.copy(mime = "audio/mp4", size = 128, subtitles = listOf(subtitle))))
        }
    }
}
