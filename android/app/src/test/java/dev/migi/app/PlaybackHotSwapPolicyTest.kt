package dev.migi.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PlaybackHotSwapPolicyTest {
    @Test
    fun replacesOnlyANewerQueueDuringAnOptedInActiveSession() {
        assertTrue(PlaybackHotSwapPolicy.shouldReplace(true, 41, 42))
        assertFalse(PlaybackHotSwapPolicy.shouldReplace(false, 41, 42))
        assertFalse(PlaybackHotSwapPolicy.shouldReplace(true, null, 42))
        assertFalse(PlaybackHotSwapPolicy.shouldReplace(true, 42, 42))
        assertFalse(PlaybackHotSwapPolicy.shouldReplace(true, 43, 42))
    }

    @Test
    fun commitsOnlyIfThePreparedQueueIsStillLatestAndPlaybackIsActive() {
        assertTrue(PlaybackHotSwapPolicy.canCommit(true, 41, 42, 42, true))
        assertFalse(PlaybackHotSwapPolicy.canCommit(true, 41, 43, 42, true))
        assertFalse(PlaybackHotSwapPolicy.canCommit(true, 42, 42, 42, true))
        assertFalse(PlaybackHotSwapPolicy.canCommit(true, 41, 42, 42, false))
        assertFalse(PlaybackHotSwapPolicy.canCommit(false, 41, 42, 42, true))
    }
}
