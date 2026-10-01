package dev.migi.app

import java.io.File
import java.util.Locale
import kotlin.math.min
import kotlin.math.sqrt

internal object ImageViewerPolicy {
    const val CACHE_DIRECTORY = "image-viewer"
    const val FILE_PREFIX = "view-"
    const val FILE_SUFFIX = ".img"
    const val MAX_IMAGE_BYTES = FileExchangeClient.MAX_FILE_BYTES
    private const val MAX_EDGE = 4096
    private const val MAX_PIXELS = 8_000_000

    fun isSupported(name: String, mime: String): Boolean {
        return when (mime.substringBefore(';').trim().lowercase(Locale.ROOT)) {
            "image/jpeg", "image/jpg", "image/png", "image/gif", "image/webp",
            "image/bmp", "image/x-ms-bmp", "image/avif" -> true
            "", "application/octet-stream" ->
                name.substringAfterLast('.', "").lowercase(Locale.ROOT) in
                    setOf("jpg", "jpeg", "png", "gif", "webp", "bmp", "avif")
            else -> false
        }
    }

    fun resolveViewerFile(cacheDirectory: File, rawPath: String?): File? {
        if (rawPath.isNullOrBlank()) return null
        return runCatching {
            val root = File(cacheDirectory, CACHE_DIRECTORY).canonicalFile
            File(rawPath).canonicalFile.takeIf {
                it.isFile && it.parentFile == root &&
                    it.name.startsWith(FILE_PREFIX) && it.name.endsWith(FILE_SUFFIX)
            }
        }.getOrNull()
    }

    // Bound decoded memory even for a small compressed file with enormous dimensions.
    fun decodeSize(width: Int, height: Int): Pair<Int, Int> {
        require(width > 0 && height > 0) { "Invalid image dimensions" }
        val ratio = min(1.0, min(
            MAX_EDGE.toDouble() / maxOf(width, height),
            sqrt(MAX_PIXELS.toDouble() / (width.toDouble() * height)),
        ))
        return maxOf(1, (width * ratio).toInt()) to maxOf(1, (height * ratio).toInt())
    }
}

internal fun SharedFile.isViewableImage(): Boolean = ImageViewerPolicy.isSupported(name, mime)
