package dev.migi.app

import java.io.ByteArrayInputStream
import java.nio.file.Files
import org.junit.Assert.*
import org.junit.Test

class VideoSubtitleFileTest {
    @Test fun rejectedImportPreservesExistingSubtitles() {
        val directory = Files.createTempDirectory("migi-subtitle").toFile()
        try {
            val destination = directory.resolve("subtitle")
            destination.writeText("existing")
            for (bytes in listOf(ByteArray(0), ByteArray(4 * 1024 * 1024 + 1))) {
                assertTrue(runCatching { VideoSubtitleFile.copy(ByteArrayInputStream(bytes), destination) }.isFailure)
                assertEquals("existing", destination.readText())
                assertFalse(directory.resolve("subtitle.new").exists())
            }
            val russian = "[Events]\nРусские субтитры"
            VideoSubtitleFile.copy(ByteArrayInputStream(russian.toByteArray()), destination)
            assertEquals(russian, destination.readText())
        } finally { directory.deleteRecursively() }
    }
}
