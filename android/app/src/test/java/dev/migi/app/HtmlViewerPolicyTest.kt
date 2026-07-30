package dev.migi.app

import java.io.File
import java.nio.file.Files
import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class HtmlViewerPolicyTest {
    @Test
    fun recognizesHtmlMimeAndExtensions() {
        assertTrue(file("report.bin", "text/html; charset=utf-8").isViewableHTML())
        assertTrue(file("report.HTML", "application/octet-stream").isViewableHTML())
        assertTrue(file("report.htm", "application/octet-stream").isViewableHTML())
        assertFalse(file("report.txt", "text/plain").isViewableHTML())
    }

    @Test
    fun acceptsOnlyOwnedViewerCacheFiles() {
        val cache = Files.createTempDirectory("migi-viewer-test-").toFile()
        try {
            val root = File(cache, HtmlViewerPolicy.CACHE_DIRECTORY).apply { mkdirs() }
            val valid = File(root, "${HtmlViewerPolicy.FILE_PREFIX}report.html").apply {
                writeText("<html></html>")
            }
            val outside = File(cache, "${HtmlViewerPolicy.FILE_PREFIX}outside.html").apply {
                writeText("<html></html>")
            }

            assertEquals(valid.canonicalFile, HtmlViewerPolicy.resolveViewerFile(cache, valid.path))
            assertNull(HtmlViewerPolicy.resolveViewerFile(cache, outside.path))
            assertNull(HtmlViewerPolicy.resolveViewerFile(cache, File(root, "other.html").path))
            assertNull(HtmlViewerPolicy.resolveViewerFile(cache, null))
        } finally {
            cache.deleteRecursively()
        }
    }

    @Test
    fun blocksExternalAndPrivilegedSubresources() {
        assertFalse(HtmlViewerPolicy.blocksSubresource("data"))
        assertFalse(HtmlViewerPolicy.blocksSubresource("blob"))
        assertFalse(HtmlViewerPolicy.blocksSubresource("about"))
        assertTrue(HtmlViewerPolicy.blocksSubresource("https"))
        assertTrue(HtmlViewerPolicy.blocksSubresource("http"))
        assertTrue(HtmlViewerPolicy.blocksSubresource("file"))
        assertTrue(HtmlViewerPolicy.blocksSubresource("content"))
        assertTrue(HtmlViewerPolicy.blocksSubresource("intent"))
        assertTrue(HtmlViewerPolicy.blocksSubresource(null))
    }

    private fun file(name: String, mime: String) = SharedFile(
        id = "file-id",
        name = name,
        mime = mime,
        size = 1,
        sha256 = "0".repeat(64),
        source = "agent:test",
        createdAt = Instant.EPOCH,
        expiresAt = Instant.EPOCH.plusSeconds(60),
    )
}
