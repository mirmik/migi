package dev.migi.g2

/** Conservative native-font grid: six body lines plus heading and page number.
 * Splits at Unicode code points, preserving whitespace and supplementary characters.
 */
object NativePager {
    fun pages(text: String): List<String> {
        val lines = text.split('\n').flatMap { line ->
            val points = line.codePoints().toArray()
            if (points.isEmpty()) listOf("") else points.toList().chunked(30).map { part ->
                String(part.toIntArray(), 0, part.size)
            }
        }
        return lines.chunked(6).map { it.joinToString("\n") }
    }
}
