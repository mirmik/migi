package dev.migi.app

import android.content.Context
import android.util.Log
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import java.time.Instant
import kotlin.math.min
import kotlin.random.Random
import org.json.JSONObject

data class AgentEvent(
    val id: Long,
    val kind: String,
    val agent: String,
    val title: String,
    val body: String,
    val createdAt: Instant,
)

internal object NativeQuicClient {
    init {
        System.loadLibrary("migi_quiche")
    }

    external fun run(
        endpoint: String,
        deviceID: String,
        certificatePin: String,
        credential: String,
        callback: NativeCallbacks,
    ): String?

    external fun pair(
        endpoint: String,
        certificatePin: String,
        secret: String,
        deviceID: String,
        deviceName: String,
    ): String
}

internal class NativeCallbacks(
    private val generation: Int,
    private val isCurrent: (Int) -> Boolean,
    private val stateConsumer: (String) -> Unit,
    private val lineConsumer: (String) -> Long,
) {
    @Volatile
    private var stopped = false

    fun stop() {
        stopped = true
    }

    fun isClosed(): Boolean = stopped || !isCurrent(generation)

    fun onState(state: String) {
        if (!isClosed()) stateConsumer(state)
    }

    fun onLine(line: String): Long = if (isClosed()) 0 else lineConsumer(line)
}

class EventStreamClient(
    private val context: Context,
    private val endpoint: String,
    private val certificatePin: String,
    private val credential: String,
    private val onState: (String) -> Unit,
    private val onEvent: (AgentEvent) -> Unit,
) : AutoCloseable {
    private val executor: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()
    private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val deviceID = DeviceIdentity.get(context)

    @Volatile
    private var closed = false
    @Volatile
    private var generation = 0
    @Volatile
    private var callbacks: NativeCallbacks? = null
    private var attempt = 0

    fun start() {
        executor.execute(::connect)
    }

    fun reconnectNow(reason: String) {
        if (closed) return
        generation++
        callbacks?.stop()
        callbacks = null
        attempt = 0
        onState(reason)
        executor.execute(::connect)
    }

    private fun connect() {
        if (closed) return
        val runGeneration = ++generation
        val runCallbacks = NativeCallbacks(
            generation = runGeneration,
            isCurrent = { !closed && generation == it },
            stateConsumer = { state ->
                if (state.startsWith("Connected with h3")) attempt = 0
                onState(state)
            },
            lineConsumer = ::consumeLine,
        )
        callbacks = runCallbacks
        val error = runCatching {
            NativeQuicClient.run(endpoint, deviceID, certificatePin, credential, runCallbacks)
        }.getOrElse {
            Log.e(TAG, "Native QUIC client failed", it)
            it.message ?: it.javaClass.simpleName
        }
        if (closed || generation != runGeneration) return
        callbacks = null
        if (error?.contains("HTTP 401") == true) {
            CredentialStore(context).clear()
            closed = true
            onState("Device credential rejected; scan a new pairing QR")
            executor.shutdown()
            return
        }
        scheduleReconnect(error ?: "Connection stopped")
    }

    private fun consumeLine(line: String): Long {
        return runCatching {
            val json = JSONObject(line)
            if (json.optString("type") == "heartbeat") return 0
            val event = AgentEvent(
                id = json.getLong("id"),
                kind = json.getString("kind"),
                agent = json.optString("agent"),
                title = json.getString("title"),
                body = json.optString("body"),
                createdAt = Instant.parse(json.getString("created_at")),
            )
            val lastID = preferences.getLong(KEY_LAST_EVENT_ID, 0)
            if (event.id <= lastID) return 0
            onEvent(event)
            if (preferences.edit().putLong(KEY_LAST_EVENT_ID, event.id).commit()) {
                event.id
            } else {
                onState("Failed to persist event cursor ${event.id}")
                0
            }
        }.getOrElse {
            onState("Ignored malformed event: ${it.message}")
            0
        }
    }

    private fun scheduleReconnect(reason: String) {
        if (closed) return
        val base = min(1L shl min(attempt, 6), 60L)
        attempt++
        val delay = base * 1000 + Random.nextLong(0, 500)
        onState("$reason; retrying in ${delay / 1000}s")
        executor.schedule(::connect, delay, TimeUnit.MILLISECONDS)
    }

    override fun close() {
        closed = true
        generation++
        callbacks?.stop()
        callbacks = null
        executor.shutdownNow()
    }

    companion object {
        const val KEY_LAST_EVENT_ID = "last_event_id"
        private const val TAG = "MigiEventStream"
    }
}
