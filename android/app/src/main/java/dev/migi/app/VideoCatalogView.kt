package dev.migi.app

internal data class VideoCollection(val id: String, val name: String, val count: Int)

/** Server membership and local files are independent views of the same media. */
internal object VideoCatalogView {
    fun collections(catalog: List<SavedPlaylistSummary>, received: List<VideoEntry>): List<VideoCollection> =
        catalog.filter { it.kind == "video" }.map { VideoCollection(it.id, it.name, it.trackCount) } +
            if (received.isEmpty()) emptyList() else listOf(VideoCollection("received", "От агента", received.size))

    fun local(archive: List<VideoEntry>, hasBytes: (PlaybackTrack) -> Boolean): List<VideoEntry> =
        archive.filter { hasBytes(it.track) }.distinctBy { it.track.sha256 }
}
