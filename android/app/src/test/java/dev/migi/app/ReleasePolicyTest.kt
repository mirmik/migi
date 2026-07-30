package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ReleasePolicyTest {
    private val pin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    private val selfPackage = "dev.migi.app"

    @Test
    fun pilotUsesPreferencePinAndNormalizesIt() {
        val colonPin = pin.chunked(2).joinToString(":").uppercase()

        assertEquals(
            pin,
            resolveReleaseSignerPin(
                packageName = "dev.migi.pilot",
                pilotPin = colonPin,
                selfPackageName = selfPackage,
                selfPin = "",
            ),
        )
    }

    @Test
    fun selfUpdateUsesOnlyCompiledPin() {
        assertEquals(
            pin,
            resolveReleaseSignerPin(
                packageName = selfPackage,
                pilotPin = "f".repeat(64),
                selfPackageName = selfPackage,
                selfPin = pin.uppercase(),
            ),
        )
    }

    @Test
    fun unexpectedPackageFailsClosed() {
        val error = assertThrows(IllegalStateException::class.java) {
            resolveReleaseSignerPin(
                packageName = "dev.example.untrusted",
                pilotPin = pin,
                selfPackageName = selfPackage,
                selfPin = pin,
            )
        }

        assertEquals("Package is not locally allowlisted", error.message)
    }

    @Test
    fun missingSelfUpdatePinFailsClosed() {
        val error = assertThrows(IllegalStateException::class.java) {
            resolveReleaseSignerPin(
                packageName = selfPackage,
                pilotPin = pin,
                selfPackageName = selfPackage,
                selfPin = "",
            )
        }

        assertEquals("Migi self-update signer pin is not configured", error.message)
    }

    @Test
    fun malformedSelfUpdatePinFailsClosed() {
        val error = assertThrows(IllegalStateException::class.java) {
            resolveReleaseSignerPin(
                packageName = selfPackage,
                pilotPin = pin,
                selfPackageName = selfPackage,
                selfPin = "not-a-certificate-digest",
            )
        }

        assertEquals("Migi self-update signer pin is malformed", error.message)
    }

    @Test
    fun missingPilotPinDoesNotFallBackToSelfPin() {
        val error = assertThrows(IllegalStateException::class.java) {
            resolveReleaseSignerPin(
                packageName = "dev.migi.pilot",
                pilotPin = null,
                selfPackageName = selfPackage,
                selfPin = pin,
            )
        }

        assertEquals("Configure the pilot signer SHA-256 in Migi", error.message)
    }
}
