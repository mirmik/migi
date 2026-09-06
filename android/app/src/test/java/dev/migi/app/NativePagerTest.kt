package dev.migi.app

import dev.migi.g2.NativePager
import org.junit.Assert.*
import org.junit.Test

class NativePagerTest {
    @Test fun unicodeSurvivesPageBoundaries() {
        val text = "Ёж😀".repeat(100)
        val pages = NativePager.pages(text)
        assertTrue(pages.size > 1)
        assertEquals(text, pages.joinToString("").replace("\n", ""))
        assertTrue(pages.all { it.lines().size <= 6 })
    }
    @Test fun explicitBlankLinesAndEmptyContentSurvive() {
        assertEquals(listOf(""), NativePager.pages(""))
        assertEquals(listOf("один\n\nдва\n"), NativePager.pages("один\n\nдва\n"))
    }
}
