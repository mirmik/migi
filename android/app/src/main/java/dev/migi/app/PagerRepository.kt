package dev.migi.app

import android.content.Context
import dev.migi.g2.PagerContent
import org.json.JSONObject

/** One atomic record is the hand-off between event ingestion and optional outputs. */
class PagerRepository(context: Context) {
    private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)

    fun current(): PagerContent? {
        val stored = preferences.getString(KEY_STATE, null)
        if (stored != null) {
            val json = JSONObject(stored)
            return PagerContent(json.getLong("id"), json.getString("title"), json.getString("body"))
        }
        // Pre-integration builds stored only the body. Preserve that current value
        // until the next real event replaces it with an identified atomic record.
        return preferences.getString(MainActivity.KEY_PAGER_MESSAGE, null)?.let { PagerContent(0, "Migi", it) }
    }

    fun accept(event: AgentEvent) = synchronized(lock) {
        val previous = current()
        if (previous != null && event.id <= previous.id) return@synchronized
        val record = JSONObject().put("id", event.id).put("title", event.title).put("body", event.body)
        check(preferences.edit().putString(KEY_STATE, record.toString())
            .putString(MainActivity.KEY_PAGER_MESSAGE, event.body).remove(DocumentRepository.KEY_ACTIVE).commit()) { "Failed to persist pager message" }
    }

    companion object {
        const val KEY_STATE = "pager_state_v1"
        private val lock = Any()
    }
}
