package dev.migi.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PackageReplacedReceiverTest {
    private val endpoint = "https://migi.example:10443"
    private val pin = "01".repeat(32)
    private val credential = "a".repeat(43)

    @Test
    fun completeStoredConnectionCanRestart() {
        assertTrue(hasStoredConnection(endpoint, pin, credential))
        assertTrue(hasStoredConnection(endpoint, pin.chunked(2).joinToString(":"), credential))
    }

    @Test
    fun unpairedOrPartialConfigurationDoesNotRestart() {
        assertFalse(hasStoredConnection(null, pin, credential))
        assertFalse(hasStoredConnection(endpoint, null, credential))
        assertFalse(hasStoredConnection(endpoint, pin, null))
        assertFalse(hasStoredConnection("http://migi.example", pin, credential))
        assertFalse(hasStoredConnection(endpoint, "bad-pin", credential))
        assertFalse(hasStoredConnection(endpoint, pin, "short"))
    }
}
