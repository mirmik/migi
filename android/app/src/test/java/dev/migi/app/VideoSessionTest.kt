package dev.migi.app

import org.junit.Assert.*
import org.junit.Test

class VideoSessionTest {
    @Test fun continuesInPlaylistOrderFromTheSelectedEpisode() {
        val session = VideoSession("episode-2", "season", "Season", listOf("episode-1", "episode-2", "episode-3"))
        assertEquals("episode-3", session.nextID())
        assertEquals("season", session.copy(trackID = session.nextID()!!).collectionID)
    }

    @Test fun finalEpisodeAndStandaloneMovieDoNotWrapToTheBeginning() {
        assertNull(VideoSession("b", "season", "Season", listOf("a", "b")).nextID())
        assertNull(VideoSession("movie", null, "Movie", listOf("movie")).nextID())
    }

    @Test fun unknownEpisodeCannotAccidentallyStartTheFirstEpisode() {
        assertNull(VideoSession("removed", "season", "Season", listOf("a", "b")).nextID())
    }
}
