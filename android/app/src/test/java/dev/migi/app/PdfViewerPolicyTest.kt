package dev.migi.app

import java.io.File
import java.nio.file.Files
import org.junit.Assert.*
import org.junit.Test

class PdfViewerPolicyTest {
    @Test
    fun detectsPdfWithGenericMimeFallback() {
        assertTrue(PdfViewerPolicy.isSupported("document.bin", "APPLICATION/PDF; charset=binary"))
        assertTrue(PdfViewerPolicy.isSupported("document.PDF", "application/octet-stream"))
        assertTrue(PdfViewerPolicy.isSupported("document.pdf", ""))
        assertFalse(PdfViewerPolicy.isSupported("document.pdf", "text/html"))
        assertFalse(PdfViewerPolicy.isSupported("document.png", "image/png"))
        assertFalse(PdfViewerPolicy.isSupported("document", "application/octet-stream"))
    }

    @Test
    fun boundsPageMemoryIncludingExtremeAspectRatios() {
        for ((width, height) in listOf(595 to 842, 100_000 to 100_000, Int.MAX_VALUE to 1, 1 to Int.MAX_VALUE)) {
            val (w, h) = PdfViewerPolicy.renderSize(width, height, Int.MAX_VALUE)
            assertTrue(w in 1..4096 && h in 1..4096)
            assertTrue(w.toLong() * h <= 8_000_000)
        }
        val (w, h) = PdfViewerPolicy.renderSize(595, 842, 2400)
        assertEquals(2400, h)
        assertEquals(595.0 / 842, w.toDouble() / h, 0.001)
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsInvalidDimensions() {
        PdfViewerPolicy.renderSize(0, 100, 2400)
    }

    @Test
    fun acceptsOnlyOwnedPdfCacheFiles() {
        val cache = Files.createTempDirectory("migi-pdf-test-").toFile()
        try {
            val root = File(cache, PdfViewerPolicy.CACHE_DIRECTORY).apply { mkdirs() }
            val valid = File(root, "view-test.pdf").apply { writeText("pdf") }
            val outside = File(cache, "view-outside.pdf").apply { writeText("pdf") }
            val wrongSuffix = File(root, "view-test.html").apply { writeText("html") }
            val link = File(root, "view-link.pdf")
            Files.createSymbolicLink(link.toPath(), outside.toPath())
            assertEquals(valid.canonicalFile, PdfViewerPolicy.resolveViewerFile(cache, valid.path))
            for (file in listOf(outside, wrongSuffix, link, File(root, "view-missing.pdf"))) {
                assertNull(PdfViewerPolicy.resolveViewerFile(cache, file.path))
            }
            assertNull(PdfViewerPolicy.resolveViewerFile(cache, null))
        } finally {
            cache.deleteRecursively()
        }
    }
}
