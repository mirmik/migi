package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Test

class PlaybackArtworkRefreshPolicyTest {
    private val artwork = PlaybackArtwork(
        id = "0123456789abcdef0123456789abcdef",
        mime = "image/jpeg",
        size = 91_145,
        sha256 = "0".repeat(64),
    )

    @Test
    fun keepsAnAlreadyRenderedArtworkDuringQueueRefresh() {
        val key = PlaybackArtworkRefreshPolicy.key(artwork)

        assertEquals(
            PlaybackArtworkRefreshAction.KEEP,
            PlaybackArtworkRefreshPolicy.action(key, artwork),
        )
    }

    @Test
    fun loadsChangedArtworkAndClearsMissingArtwork() {
        assertEquals(
            PlaybackArtworkRefreshAction.LOAD,
            PlaybackArtworkRefreshPolicy.action(null, artwork),
        )
        assertEquals(
            PlaybackArtworkRefreshAction.CLEAR,
            PlaybackArtworkRefreshPolicy.action("previous", null),
        )
    }
}
