package dev.migi.app

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import android.content.ContentValues
import dev.migi.documents.ReadingDocument
import dev.migi.g2.PagerContent

/** Full content is committed before EventStreamClient advances its delivery cursor. */
class DocumentRepository(private val context: Context) {
    private val db get() = helper(context).writableDatabase
    private val prefs = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    data class Entry(val eventId: Long, val title: String, val agent: String, val page: Int)
    fun accept(event: AgentEvent): Boolean = synchronized(lock) {
        val document = ReadingDocument.parse(event.body)
        var created = false
        db.beginTransaction()
        try {
            val values = ContentValues().apply {
                put("event_id", event.id); put("title", document.title); put("agent", event.agent)
                put("body", event.body); put("created_at", event.createdAt.toString())
            }
            created = db.insertWithOnConflict("documents", null, values, SQLiteDatabase.CONFLICT_IGNORE) != -1L
            db.setTransactionSuccessful()
        } finally { db.endTransaction() }
        // Even on replay, repair a crash between the SQLite commit and the selection update.
        val handled = prefs.getLong(KEY_HANDLED, 0)
        if (event.id > handled) {
            check(prefs.edit().putLong(KEY_HANDLED, event.id).putLong(KEY_ACTIVE, event.id)
                .putLong(KEY_GENERATION, prefs.getLong(KEY_GENERATION, 0) + 1).commit())
        }
        created
    }
    fun entries(): List<Entry> = db.rawQuery("SELECT event_id,title,agent,page FROM documents ORDER BY event_id DESC", null).use { c ->
        buildList { while (c.moveToNext()) add(Entry(c.getLong(0), c.getString(1), c.getString(2), c.getInt(3))) }
    }
    fun content(id: Long): PagerContent? = db.rawQuery("SELECT title,body,page FROM documents WHERE event_id=?", arrayOf(id.toString())).use { c ->
        if (!c.moveToFirst()) null else PagerContent(id, c.getString(0), c.getString(0), ReadingDocument.parse(c.getString(1)), c.getInt(2))
    }
    fun active(): PagerContent? = prefs.getLong(KEY_ACTIVE, 0).takeIf { it > 0 }?.let(::content)
    fun select(id: Long) {
        require(content(id) != null)
        check(prefs.edit().putLong(KEY_ACTIVE, id).putLong(KEY_GENERATION, prefs.getLong(KEY_GENERATION, 0) + 1).commit())
    }
    fun savePage(id: Long, page: Int) {
        require(page >= 0)
        db.execSQL("UPDATE documents SET page=? WHERE event_id=? AND page<>?", arrayOf(page, id, page))
    }
    fun returnToPager() {
        check(prefs.edit().remove(KEY_ACTIVE).putLong(KEY_GENERATION, prefs.getLong(KEY_GENERATION, 0) + 1).commit())
    }
    companion object {
        const val EVENT_KIND = "document.published"
        const val KEY_ACTIVE = "reading_document_event"
        const val KEY_GENERATION = "reading_documents_generation"
        private const val KEY_HANDLED = "reading_documents_handled"
        private val lock = Any()
        @Volatile private var instance: Store? = null
        private fun helper(context: Context): Store = instance ?: synchronized(lock) {
            instance ?: Store(context.applicationContext).also { instance = it }
        }
    }
    private class Store(context: Context) : SQLiteOpenHelper(context, "reading-documents.db", null, 1) {
        override fun onCreate(db: SQLiteDatabase) {
            db.execSQL("CREATE TABLE documents(event_id INTEGER PRIMARY KEY,title TEXT NOT NULL,agent TEXT NOT NULL,body TEXT NOT NULL,created_at TEXT NOT NULL,page INTEGER NOT NULL DEFAULT 0)")
        }
        override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) = Unit
    }
}
