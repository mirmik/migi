package dev.migi.app

import java.io.File
import java.util.Locale
import kotlin.math.min
import kotlin.math.sqrt

internal object PdfViewerPolicy {
    const val CACHE_DIRECTORY = "pdf-viewer"
    const val FILE_PREFIX = "view-"
    const val FILE_SUFFIX = ".pdf"
    const val MAX_PDF_BYTES = FileExchangeClient.MAX_FILE_BYTES

    fun isSupported(name: String, mime: String): Boolean =
        when (mime.substringBefore(';').trim().lowercase(Locale.ROOT)) {
            "application/pdf" -> true
            "", "application/octet-stream" -> name.endsWith(".pdf", ignoreCase = true)
            else -> false
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

    // A single page uses at most 32 MB, regardless of the PDF's dimensions.
    fun renderSize(width: Int, height: Int, targetEdge: Int): Pair<Int, Int> {
        require(width > 0 && height > 0)
        val ratio = min(
            targetEdge.coerceIn(1600, 4096).toDouble() / maxOf(width, height),
            sqrt(8_000_000.0 / (width.toDouble() * height)),
        )
        return maxOf(1, (width * ratio).toInt()) to maxOf(1, (height * ratio).toInt())
    }
}

internal fun SharedFile.isViewablePdf(): Boolean = PdfViewerPolicy.isSupported(name, mime)
