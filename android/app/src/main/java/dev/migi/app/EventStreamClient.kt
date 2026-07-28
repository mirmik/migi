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
    val artifact: ArtifactReference? = null,
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

    external fun releaseMetadata(
        endpoint: String,
        certificatePin: String,
        credential: String,
        artifactID: String,
    ): String

    external fun downloadRelease(
        endpoint: String,
        certificatePin: String,
        credential: String,
        artifactID: String,
        fileDescriptor: Int,
        maxBytes: Long,
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
    private val releases = ReleaseRepository(context)
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
                artifact = json.optJSONObject("artifact")?.let {
                    ArtifactReference(
                        id = it.getString("id"),
                        packageName = it.getString("package_name"),
                        versionCode = it.getLong("version_code"),
                        versionName = it.getString("version_name"),
                    )
                },
            )
            val lastID = releases.lastEventID()
            if (event.id <= lastID) return event.id
            if (event.kind == FILTERED_EVENT_KIND) {
                if (releases.advanceEventCursor(event.id)) event.id else 0
            } else if (event.kind == RELEASE_EVENT_KIND) {
                require(event.artifact != null) { "Release event has no artifact reference" }
                releases.recordReleaseAndAdvance(event)
                runCatching { onEvent(event) }
                    .onFailure { Log.e(TAG, "Failed to notify for durable release ${event.artifact.id}", it) }
                event.id
            } else {
                onEvent(event)
                if (releases.advanceEventCursor(event.id)) event.id else 0
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
        private const val RELEASE_EVENT_KIND = "app.update_available"
        private const val FILTERED_EVENT_KIND = "internal.filtered"
        private const val TAG = "MigiEventStream"
    }
}
