package dev.migi.documents

/** Geometry independent of Android graphics, so page boundaries can be tested. */
object DocumentPagination {
    const val LINE_HEIGHT = 27
    const val BODY_HEIGHT = 210
    data class Unit(val text: String? = null, val formula: String? = null, val height: Int, val heading: Boolean = false)

    fun wrap(text: String, columns: Int = 40): List<String> = text.replace('\t', ' ').split('\n').flatMap { original ->
        val result = mutableListOf<String>()
        var remaining = original
        while (remaining.codePointCount(0, remaining.length) > columns) {
            val end = remaining.offsetByCodePoints(0, columns)
            val space = remaining.lastIndexOf(' ', end - 1)
            val cut = if (space > end / 2) space + 1 else end
            result += remaining.substring(0, cut)
            remaining = remaining.substring(cut)
        }
        result += remaining
        result
    }

    fun paginate(units: List<Unit>): List<List<Unit>> {
        val pages = mutableListOf<List<Unit>>()
        var page = mutableListOf<Unit>()
        var height = 0
        var textCount = 0
        var imageCount = 0
        fun flush() { if (page.isNotEmpty()) pages += page.toList(); page = mutableListOf(); height = 0; textCount = 0; imageCount = 0 }
        units.forEachIndexed { index, unit ->
            require(unit.height in 1..BODY_HEIGHT)
            // Keep a heading with the following block when the pair can fit.
            val next = units.getOrNull(index + 1)
            val needed = if (unit.heading && next != null && unit.height + next.height <= BODY_HEIGHT) unit.height + next.height else unit.height
            val newText = if (unit.text != null) 1 else 0
            val newImage = if (unit.formula != null) 1 else 0
            if (height + needed > BODY_HEIGHT || textCount + newText > 6 || imageCount + newImage > 3) flush()
            page += unit; height += unit.height; textCount += newText; imageCount += newImage
        }
        flush()
        return pages.ifEmpty { listOf(emptyList()) }
    }
}
