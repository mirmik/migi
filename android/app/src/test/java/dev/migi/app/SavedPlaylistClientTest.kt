package dev.migi.app

import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class SavedPlaylistClientTest {
    @Test
    fun acceptsDeviceSafePlaylistSummary() {
        val playlist = SavedPlaylistSummary(
            id = "0123456789abcdef0123456789abcdef",
            name = "Long album",
            trackCount = 40,
            updatedAt = Instant.parse("2026-08-28T12:00:00Z"),
        )
        assertEquals(playlist, SavedPlaylistClient.validate(playlist))
    }

    @Test
    fun rejectsMalformedPlaylistSummary() {
        assertThrows(IllegalArgumentException::class.java) {
            SavedPlaylistClient.validate(SavedPlaylistSummary(
                id = "../../playlist",
                name = "Unsafe",
                trackCount = 1,
                updatedAt = Instant.parse("2026-08-28T12:00:00Z"),
            ))
        }
    }

    @Test
    fun rejectsNativeClientError() {
        assertThrows(IllegalStateException::class.java) {
            SavedPlaylistClient.checkResponse("MIGI_ERROR:request failed")
        }
    }
}
