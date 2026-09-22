package dev.migi.app

/** A viewing context survives catalog changes and process recreation. */
internal data class VideoSession(
    val trackID: String,
    val collectionID: String?,
    val collectionName: String,
    val orderedIDs: List<String>,
) {
    fun nextID(): String? {
        val index = orderedIDs.indexOf(trackID)
        return if (index >= 0) orderedIDs.getOrNull(index + 1) else null
    }
}
