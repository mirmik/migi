package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Test

class ReleaseChangesTest {
    @Test
    fun subscriptionReceivesChangesOnlyUntilClosed() {
        var calls = 0
        val subscription = ReleaseChanges.subscribe { calls++ }

        ReleaseChanges.publish()
        subscription.close()
        ReleaseChanges.publish()

        assertEquals(1, calls)
    }

    @Test
    fun failingListenerDoesNotBlockOtherSubscribers() {
        var calls = 0
        val failing = ReleaseChanges.subscribe { error("expected") }
        val healthy = ReleaseChanges.subscribe { calls++ }

        try {
            ReleaseChanges.publish()
        } finally {
            failing.close()
            healthy.close()
        }

        assertEquals(1, calls)
    }
}
