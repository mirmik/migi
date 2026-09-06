package dev.migi.app

import dev.migi.documents.DocumentPagination as P
import org.junit.Assert.*
import org.junit.Test

class DocumentPaginationTest {
    @Test fun preservesUnicodeAndWhitespaceAcrossWrapping() {
        val text = "Ёжик 😀 ∑ ".repeat(40) + "конец"
        assertEquals(text, P.wrap(text).joinToString(""))
        assertTrue(P.wrap(text).all { it.codePointCount(0, it.length) <= 40 })
    }
    @Test fun movesHeadingWithFollowingFormulaAndPreservesOrder() {
        val first = P.Unit(text="paragraph", height=170)
        val heading = P.Unit(text="heading", height=32, heading=true)
        val formula = P.Unit(formula="x^2", height=100)
        val pages = P.paginate(listOf(first, heading, formula))
        assertEquals(listOf(listOf(first), listOf(heading,formula)), pages)
    }
    @Test fun respectsHeightAndContainerBudgetsWithoutDroppingBlocks() {
        val units = (0..35).map { if (it % 3 == 0) P.Unit(formula="x_$it",height=40) else P.Unit(text="line$it",height=32) }
        val pages = P.paginate(units)
        assertEquals(units, pages.flatten())
        assertTrue(pages.all { p -> p.sumOf { it.height } <= P.BODY_HEIGHT && p.count { it.text != null } <= 6 && p.count { it.formula != null } <= 3 })
    }
}
