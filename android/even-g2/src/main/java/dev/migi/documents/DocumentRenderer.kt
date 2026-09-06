package dev.migi.documents

import android.content.Context
import com.faceclaw.app.NativePage

object DocumentRenderer {
    fun render(context: Context, document: ReadingDocument, identity: String): List<NativePage> {
        val formulas = mutableMapOf<String, Triple<Int, Int, ByteArray>>()
        val units = mutableListOf<DocumentPagination.Unit>()
        fun text(value: String, heading: Boolean = false) {
            DocumentPagination.wrap(value).chunked(if (heading) 2 else 3).forEach { lines ->
                units += DocumentPagination.Unit(text = lines.joinToString("\n"), height = lines.size * 27 + 5, heading = heading)
            }
        }
        for ((blockIndex, block) in document.blocks.withIndex()) when (block.type) {
            "heading" -> if (blockIndex != 0 || block.text != document.title) text(block.text, true)
            "paragraph" -> text(block.text)
            "list" -> block.items.forEach { text("• $it") }
            "math" -> {
                try {
                    val formula = formulas.getOrPut(block.latex) {
                        val result = FormulaRenderer.render(context, block.latex)
                        try { Triple(result.bitmap.width, result.bitmap.height, result.bmp) } finally { result.bitmap.recycle() }
                    }
                    units += DocumentPagination.Unit(formula = block.latex, height = formula.second + 8)
                } catch (e: Exception) {
                    text("Формула: ${block.latex}")
                    text("[${e.message?.take(80) ?: "Ошибка отображения формулы"}]")
                }
            }
        }
        val pages = DocumentPagination.paginate(units)
        return pages.mapIndexed { index, page ->
            val texts = mutableListOf(NativePage.Text(16, 4, 544, 30, DocumentPagination.wrap(document.title).first()))
            val images = mutableListOf<NativePage.Image>()
            var y = 39
            for (unit in page) {
                if (unit.text != null) {
                    val previous = texts.lastOrNull()
                    if (texts.size > 1 && previous != null && previous.y + previous.height == y) {
                        texts[texts.lastIndex] = NativePage.Text(16, previous.y, 544,
                            previous.height + unit.height, previous.text + "\n" + unit.text)
                    } else texts += NativePage.Text(16, y, 544, unit.height, unit.text)
                }
                if (unit.formula != null) {
                    val (w, h, bmp) = formulas.getValue(unit.formula)
                    images += NativePage.Image((576 - w) / 2, y, w, h, bmp)
                }
                y += unit.height
            }
            texts += NativePage.Text(16, 258, 544, 30, "${index + 1}/${pages.size} • касание: далее")
            NativePage("document:$identity:$index", texts.toTypedArray(), images.toTypedArray())
        }
    }
}
