package dev.migi.app

import dev.migi.g2.NativePager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class VoiceReviewPaginationTest {
    @Test fun fullTranscriptSurvivesReviewPagination() {
        val text = ("Проверка речи 🦡 ".repeat(70)).trim()
        val pages = NativePager.pages(text, 4)
        assertTrue(pages.size > 1)
        assertTrue(pages.all { it.lines().size <= 4 })
        assertEquals(text, pages.joinToString("").replace("\n", ""))
    }
}
