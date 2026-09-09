package dev.migi.g2

/** Conservative native-font grid: six body lines plus heading and page number.
 * Splits at Unicode code points, preserving whitespace and supplementary characters.
 */
object NativePager {
    fun pages(text: String, bodyLines: Int = 6): List<String> {
        require(bodyLines in 1..6)
        val lines = text.split('\n').flatMap { line ->
            val points = line.codePoints().toArray()
            if (points.isEmpty()) listOf("") else points.toList().chunked(30).map { part ->
                String(part.toIntArray(), 0, part.size)
            }
        }
        return lines.chunked(bodyLines).map { it.joinToString("\n") }
    }
}
