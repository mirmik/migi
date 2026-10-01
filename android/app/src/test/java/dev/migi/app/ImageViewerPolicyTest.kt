package dev.migi.app

import java.io.File
import java.nio.file.Files
import org.junit.Assert.*
import org.junit.Test

class ImageViewerPolicyTest {
    @Test
    fun supportsRasterImagesAndGenericMimeFallback() {
        assertTrue(ImageViewerPolicy.isSupported("photo.bin", "IMAGE/JPEG; charset=binary"))
        for (extension in listOf("JPG", "jpeg", "png", "gif", "webp", "bmp", "avif")) {
            assertTrue(ImageViewerPolicy.isSupported("photo.$extension", "application/octet-stream"))
        }
        assertFalse(ImageViewerPolicy.isSupported("photo.png", "text/html"))
        assertFalse(ImageViewerPolicy.isSupported("photo.jpg", "image/svg+xml"))
        assertFalse(ImageViewerPolicy.isSupported("photo.svg", "application/octet-stream"))
        assertFalse(ImageViewerPolicy.isSupported("note.txt", "text/plain"))
    }

    @Test
    fun limitsDecodedMemoryWithoutUpscaling() {
        assertEquals(640 to 480, ImageViewerPolicy.decodeSize(640, 480))
        for ((width, height) in listOf(100_000 to 100_000, Int.MAX_VALUE to 1, 1 to Int.MAX_VALUE, 8000 to 6000)) {
            val (w, h) = ImageViewerPolicy.decodeSize(width, height)
            assertTrue(w in 1..4096 && h in 1..4096)
            assertTrue(w.toLong() * h <= 8_000_000)
        }
    }

    @Test
    fun acceptsOnlyOwnedViewerCacheFiles() {
        val cache = Files.createTempDirectory("migi-image-viewer-test-").toFile()
        try {
            val root = File(cache, ImageViewerPolicy.CACHE_DIRECTORY).apply { mkdirs() }
            val valid = File(root, "view-test.img").apply { writeText("image") }
            val outside = File(cache, "view-outside.img").apply { writeText("image") }
            val link = File(root, "view-link.img")
            Files.createSymbolicLink(link.toPath(), outside.toPath())
            assertEquals(valid.canonicalFile, ImageViewerPolicy.resolveViewerFile(cache, valid.path))
            assertNull(ImageViewerPolicy.resolveViewerFile(cache, outside.path))
            assertNull(ImageViewerPolicy.resolveViewerFile(cache, link.path))
            assertNull(ImageViewerPolicy.resolveViewerFile(cache, File(root, "view-missing.img").path))
            assertNull(ImageViewerPolicy.resolveViewerFile(cache, null))
        } finally {
            cache.deleteRecursively()
        }
    }
}
