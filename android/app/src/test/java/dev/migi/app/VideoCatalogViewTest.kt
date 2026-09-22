package dev.migi.app

import java.time.Instant
import org.junit.Assert.*
import org.junit.Test

class VideoCatalogViewTest {
    private fun playlist(id: Char, name: String = "Season") = SavedPlaylistSummary(
        id.toString().repeat(32), name, 12, Instant.EPOCH, "video")
    private fun entry(id: Char, digest: Char = id) = VideoEntry(
        PlaybackTrack(id.toString().repeat(32), "Episode", "", "video/mp4", 100,
            digest.toString().repeat(64)), "Deleted test playlist")

    @Test fun deletedHistoryCannotRepopulateCatalogAndDownloadsSurvive() {
        val archive = listOf(entry('a'), entry('b'), entry('c'), entry('d'), entry('e'))
        val catalog = listOf(playlist('a'), playlist('b'))
        assertEquals(listOf("a".repeat(32), "b".repeat(32)),
            VideoCatalogView.collections(catalog, emptyList()).map { it.id })
        // A downloaded file from a deleted playlist remains independently accessible.
        assertEquals(listOf(archive[3]), VideoCatalogView.local(archive) { it.id == "d".repeat(32) })
        assertTrue(VideoCatalogView.collections(emptyList(), emptyList()).isEmpty())
    }

    @Test fun sameNamedPlaylistsRemainDistinctAndNamesCanChange() {
        val a = playlist('a'); val b = playlist('b')
        assertEquals(2, VideoCatalogView.collections(listOf(a, b), emptyList()).size)
        val renamed = VideoCatalogView.collections(listOf(a.copy(name = "Renamed"), b), emptyList())
        assertEquals(a.id, renamed[0].id)
        assertEquals("Renamed", renamed[0].name)
    }

    @Test fun sharedPhysicalFileAppearsOnceAmongDownloadsIncludingPartialFiles() {
        val archive = listOf(entry('a'), entry('b', 'a'), entry('c'))
        assertEquals(listOf(archive[0]), VideoCatalogView.local(archive) { it.sha256 == "a".repeat(64) })
    }

    @Test fun directSendsHaveExplicitInboxAndMusicIsExcluded() {
        val cards = VideoCatalogView.collections(listOf(playlist('a').copy(kind = "audio")), listOf(entry('b')))
        assertEquals(listOf(VideoCollection("received", "От агента", 1)), cards)
    }
}
