package dev.migi.app

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.SoundPool
import android.os.SystemClock
import android.util.Log
import java.time.Duration
import java.time.Instant
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

/**
 * Routes event semantics to short local audio cues.
 *
 * Voice messages deliberately do not go through SoundPool: they will use a
 * streaming player fed by authenticated media metadata. Keeping this boundary
 * here prevents transport and notification code from depending on a concrete
 * audio implementation.
 */
internal class EventAudioPlayer(context: Context) : AutoCloseable {
    private enum class Cue(val resource: Int, val durationMillis: Long) {
        COMPLETED(R.raw.cue_completed, 830),
        ATTENTION(R.raw.cue_attention, 950),
        PAGER(R.raw.cue_pager, 1_030),
    }

    private val lock = Any()
    private val audioAttributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_NOTIFICATION_EVENT)
        .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
        .build()
    private val audioManager = context.getSystemService(AudioManager::class.java)
    private val audioFocusRequest = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
        .setAudioAttributes(audioAttributes)
        .build()
    private val soundPool = SoundPool.Builder()
        .setMaxStreams(1)
        .setAudioAttributes(audioAttributes)
        .build()
    private val soundIDs = mutableMapOf<Cue, Int>()
    private val loaded = mutableSetOf<Int>()
    private val playbackExecutor: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()
    private var nextPlaybackAt = 0L
    private var closed = false

    init {
        soundPool.setOnLoadCompleteListener { _, soundID, status ->
            synchronized(lock) {
                if (!closed && status == 0) loaded += soundID
            }
        }
        for (cue in Cue.entries) {
            soundIDs[cue] = soundPool.load(context, cue.resource, 1)
        }
    }

    fun play(event: AgentEvent) {
        val cue = cueFor(event) ?: return
        val delay = synchronized(lock) {
            if (closed) return
            val now = SystemClock.elapsedRealtime()
            val scheduledAt = maxOf(now, nextPlaybackAt)
            nextPlaybackAt = scheduledAt + cue.durationMillis + CUE_GAP_MILLIS
            scheduledAt - now
        }
        playbackExecutor.schedule(
            { playWhenLoaded(cue, event.id, LOAD_RETRIES) },
            delay,
            TimeUnit.MILLISECONDS,
        )
    }

    private fun playWhenLoaded(cue: Cue, eventID: Long, retriesLeft: Int) {
        val soundID = synchronized(lock) {
            if (closed) return
            soundIDs[cue]?.takeIf { it in loaded }
        }
        if (soundID == null) {
            if (retriesLeft > 0) {
                playbackExecutor.schedule(
                    { playWhenLoaded(cue, eventID, retriesLeft - 1) },
                    LOAD_RETRY_MILLIS,
                    TimeUnit.MILLISECONDS,
                )
            } else {
                Log.w(TAG, "Audio cue ${cue.name} was not loaded for event $eventID")
            }
            return
        }
        val focus = audioManager.requestAudioFocus(audioFocusRequest)
        if (focus != AudioManager.AUDIOFOCUS_REQUEST_GRANTED) {
            Log.w(TAG, "Audio focus was not granted for ${cue.name}, event $eventID")
        }
        val streamID = soundPool.play(soundID, 1f, 1f, 1, 0, 1f)
        if (streamID == 0) {
            audioManager.abandonAudioFocusRequest(audioFocusRequest)
            Log.w(TAG, "Audio cue ${cue.name} failed to start for event $eventID")
        } else {
            Log.i(TAG, "Playing audio cue ${cue.name} for event $eventID")
            playbackExecutor.schedule(
                { audioManager.abandonAudioFocusRequest(audioFocusRequest) },
                cue.durationMillis + FOCUS_RELEASE_DELAY_MILLIS,
                TimeUnit.MILLISECONDS,
            )
        }
    }

    private fun cueFor(event: AgentEvent): Cue? {
        val age = Duration.between(event.createdAt, Instant.now())
        if (age.isNegative || age > MAX_CUE_AGE) return null
        return when (event.kind) {
            "agent.completed" -> Cue.COMPLETED
            "agent.attention_required" -> Cue.ATTENTION
            "pager.message" -> if (event.body.isBlank()) null else Cue.PAGER
            else -> Cue.ATTENTION
        }
    }

    override fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
        }
        playbackExecutor.shutdownNow()
        soundPool.release()
    }

    companion object {
        private val MAX_CUE_AGE: Duration = Duration.ofMinutes(5)
        private const val CUE_GAP_MILLIS = 150L
        private const val LOAD_RETRY_MILLIS = 50L
        private const val LOAD_RETRIES = 20
        private const val FOCUS_RELEASE_DELAY_MILLIS = 50L
        private const val TAG = "MigiAudio"
    }
}
