package dev.migi.app

internal enum class PlaybackArtworkRefreshAction {
    KEEP,
    CLEAR,
    LOAD,
}

internal object PlaybackArtworkRefreshPolicy {
    fun action(loadedKey: String?, artwork: PlaybackArtwork?): PlaybackArtworkRefreshAction = when {
        artwork == null -> PlaybackArtworkRefreshAction.CLEAR
        loadedKey == key(artwork) -> PlaybackArtworkRefreshAction.KEEP
        else -> PlaybackArtworkRefreshAction.LOAD
    }

    fun key(artwork: PlaybackArtwork): String = "${artwork.id}:${artwork.sha256}"
}
