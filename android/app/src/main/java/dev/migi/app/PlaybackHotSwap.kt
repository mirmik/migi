package dev.migi.app

internal object PlaybackHotSwapPolicy {
    fun shouldReplace(
        enabled: Boolean,
        activeEventID: Long?,
        incomingEventID: Long,
    ): Boolean = enabled && activeEventID != null && incomingEventID > activeEventID

    fun canCommit(
        enabled: Boolean,
        activeEventID: Long?,
        latestEventID: Long?,
        incomingEventID: Long,
        hasMedia: Boolean,
    ): Boolean = hasMedia &&
        latestEventID == incomingEventID &&
        shouldReplace(enabled, activeEventID, incomingEventID)
}
