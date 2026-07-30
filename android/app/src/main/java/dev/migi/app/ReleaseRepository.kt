package dev.migi.app

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import java.time.Instant
import java.util.concurrent.CopyOnWriteArraySet

data class ArtifactReference(
    val id: String,
    val packageName: String,
    val versionCode: Long,
    val versionName: String,
)

data class PendingRelease(
    val artifact: ArtifactReference,
    val eventID: Long,
    val state: String,
    val size: Long?,
    val sha256: String?,
    val publisher: String?,
    val releaseNotes: String?,
    val tempPath: String?,
    val sessionID: Int?,
    val error: String?,
)

class ReleaseRepository(context: Context) : SQLiteOpenHelper(
    context.applicationContext,
    DATABASE_NAME,
    null,
    DATABASE_VERSION,
) {
    override fun onCreate(database: SQLiteDatabase) {
        database.execSQL(
            """
            CREATE TABLE client_state (
                key TEXT PRIMARY KEY,
                value INTEGER NOT NULL
            )
            """.trimIndent(),
        )
        database.execSQL(
            """
            CREATE TABLE releases (
                artifact_id TEXT PRIMARY KEY,
                event_id INTEGER NOT NULL UNIQUE,
                package_name TEXT NOT NULL,
                version_code INTEGER NOT NULL,
                version_name TEXT NOT NULL,
                state TEXT NOT NULL,
                size INTEGER,
                sha256 TEXT,
                publisher TEXT,
                release_notes TEXT,
                source_revision TEXT,
                build_id TEXT,
                temp_path TEXT,
                session_id INTEGER,
                error TEXT,
                updated_at TEXT NOT NULL
            )
            """.trimIndent(),
        )
    }

    override fun onUpgrade(database: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
        error("No release database migration from $oldVersion to $newVersion")
    }

    fun lastEventID(): Long = readableDatabase.rawQuery(
        "SELECT value FROM client_state WHERE key = ?",
        arrayOf(KEY_EVENT_CURSOR),
    ).use { cursor ->
        if (cursor.moveToFirst()) cursor.getLong(0) else 0L
    }

    fun recordReleaseAndAdvance(event: AgentEvent): Boolean {
        val artifact = requireNotNull(event.artifact)
        val database = writableDatabase
        database.beginTransaction()
        val changed = try {
            val current = readCursor(database)
            if (event.id <= current) {
                false
            } else {
                val values = ContentValues().apply {
                    put("artifact_id", artifact.id)
                    put("event_id", event.id)
                    put("package_name", artifact.packageName)
                    put("version_code", artifact.versionCode)
                    put("version_name", artifact.versionName)
                    put("state", STATE_PENDING)
                    put("updated_at", Instant.now().toString())
                }
                database.insertWithOnConflict(
                    "releases",
                    null,
                    values,
                    SQLiteDatabase.CONFLICT_IGNORE,
                )
                advanceCursor(database, event.id)
                database.setTransactionSuccessful()
                true
            }
        } finally {
            database.endTransaction()
        }
        if (changed) ReleaseChanges.publish()
        return changed
    }

    fun advanceEventCursor(eventID: Long): Boolean {
        val database = writableDatabase
        database.beginTransaction()
        return try {
            val changed = eventID > readCursor(database)
            if (changed) advanceCursor(database, eventID)
            database.setTransactionSuccessful()
            changed
        } finally {
            database.endTransaction()
        }
    }

    fun resetForPairing() {
        val database = writableDatabase
        database.beginTransaction()
        try {
            database.delete("releases", null, null)
            database.delete("client_state", null, null)
            database.setTransactionSuccessful()
        } finally {
            database.endTransaction()
        }
        ReleaseChanges.publish()
    }

    fun listReleases(): List<PendingRelease> = readableDatabase.rawQuery(
        """
        SELECT artifact_id, event_id, package_name, version_code, version_name,
               state, size, sha256, publisher, release_notes,
               temp_path, session_id, error
        FROM releases
        ORDER BY event_id DESC
        """.trimIndent(),
        null,
    ).use { cursor ->
        buildList {
            while (cursor.moveToNext()) {
                add(
                    PendingRelease(
                        artifact = ArtifactReference(
                            id = cursor.getString(0),
                            packageName = cursor.getString(2),
                            versionCode = cursor.getLong(3),
                            versionName = cursor.getString(4),
                        ),
                        eventID = cursor.getLong(1),
                        state = cursor.getString(5),
                        size = cursor.nullableLong(6),
                        sha256 = cursor.nullableString(7),
                        publisher = cursor.nullableString(8),
                        releaseNotes = cursor.nullableString(9),
                        tempPath = cursor.nullableString(10),
                        sessionID = cursor.nullableInt(11),
                        error = cursor.nullableString(12),
                    ),
                )
            }
        }
    }

    fun findRelease(artifactID: String): PendingRelease? =
        listReleases().firstOrNull { it.artifact.id == artifactID }

    fun updateMetadata(artifactID: String, release: ReleaseMetadata) {
        require(artifactID == release.artifactID)
        val values = ContentValues().apply {
            put("size", release.size)
            put("sha256", release.sha256)
            put("publisher", release.publisher)
            put("release_notes", release.releaseNotes)
            put("source_revision", release.sourceRevision)
            put("build_id", release.buildID)
            put("updated_at", Instant.now().toString())
        }
        check(writableDatabase.update(
            "releases",
            values,
            "artifact_id = ? AND package_name = ? AND version_code = ?",
            arrayOf(artifactID, release.packageName, release.versionCode.toString()),
        ) == 1) { "Pending release identity changed" }
        ReleaseChanges.publish()
    }

    fun updateState(
        artifactID: String,
        state: String,
        tempPath: String? = null,
        sessionID: Int? = null,
        error: String? = null,
    ) {
        require(state in VALID_STATES)
        val values = ContentValues().apply {
            put("state", state)
            if (tempPath == null) putNull("temp_path") else put("temp_path", tempPath)
            if (sessionID == null) putNull("session_id") else put("session_id", sessionID)
            if (error == null) putNull("error") else put("error", error)
            put("updated_at", Instant.now().toString())
        }
        check(writableDatabase.update(
            "releases",
            values,
            "artifact_id = ?",
            arrayOf(artifactID),
        ) == 1) { "Unknown release $artifactID" }
        ReleaseChanges.publish()
    }

    private fun readCursor(database: SQLiteDatabase): Long = database.rawQuery(
        "SELECT value FROM client_state WHERE key = ?",
        arrayOf(KEY_EVENT_CURSOR),
    ).use { cursor ->
        if (cursor.moveToFirst()) cursor.getLong(0) else 0L
    }

    private fun advanceCursor(database: SQLiteDatabase, eventID: Long) {
        database.execSQL(
            """
            INSERT INTO client_state(key, value) VALUES(?, ?)
            ON CONFLICT(key) DO UPDATE SET value = max(client_state.value, excluded.value)
            """.trimIndent(),
            arrayOf<Any>(KEY_EVENT_CURSOR, eventID),
        )
    }

    private fun android.database.Cursor.nullableString(index: Int): String? =
        if (isNull(index)) null else getString(index)

    private fun android.database.Cursor.nullableLong(index: Int): Long? =
        if (isNull(index)) null else getLong(index)

    private fun android.database.Cursor.nullableInt(index: Int): Int? =
        if (isNull(index)) null else getInt(index)

    companion object {
        private const val DATABASE_NAME = "migi-releases.db"
        private const val DATABASE_VERSION = 1
        private const val KEY_EVENT_CURSOR = "event_cursor"

        const val STATE_PENDING = "pending"
        const val STATE_DOWNLOADING = "downloading"
        const val STATE_DOWNLOADED = "downloaded"
        const val STATE_INSTALLING = "installing"
        const val STATE_INSTALLED = "installed"
        const val STATE_FAILED = "failed"
        const val STATE_DISMISSED = "dismissed"

        private val VALID_STATES = setOf(
            STATE_PENDING,
            STATE_DOWNLOADING,
            STATE_DOWNLOADED,
            STATE_INSTALLING,
            STATE_INSTALLED,
            STATE_FAILED,
            STATE_DISMISSED,
        )
    }
}

internal object ReleaseChanges {
    private val listeners = CopyOnWriteArraySet<() -> Unit>()

    fun subscribe(listener: () -> Unit): AutoCloseable {
        listeners.add(listener)
        return AutoCloseable { listeners.remove(listener) }
    }

    fun publish() {
        listeners.forEach { listener ->
            runCatching(listener)
        }
    }
}

data class ReleaseMetadata(
    val artifactID: String,
    val packageName: String,
    val versionCode: Long,
    val versionName: String,
    val size: Long,
    val sha256: String,
    val publisher: String,
    val releaseNotes: String,
    val sourceRevision: String,
    val buildID: String,
)
