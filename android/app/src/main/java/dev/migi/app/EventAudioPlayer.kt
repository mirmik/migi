package dev.migi.app

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.SoundPool
import android.os.SystemClock
import android.util.Log
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
    private val appContext = context.applicationContext

    internal enum class Cue(
        val resource: Int,
        val durationMillis: Long,
        val legacyBypassChannelID: String,
    ) {
        COMPLETED(
            R.raw.cue_complete,
            1_590,
            "agent-completed-dnd-v1",
        ),
        ATTENTION(
            R.raw.cue_complete,
            1_590,
            "agent-attention-dnd-v1",
        ),
        PAGER(
            R.raw.cue_complete,
            1_590,
            "pager-message-dnd-v1",
        ),
    }

    private inner class Output(usage: Int) : AutoCloseable {
        val audioAttributes: AudioAttributes = AudioAttributes.Builder()
            .setUsage(usage)
            .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
            .build()
        val audioFocusRequest: AudioFocusRequest =
            AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
                .setAudioAttributes(audioAttributes)
                .build()
        val soundPool: SoundPool = SoundPool.Builder()
            .setMaxStreams(1)
            .setAudioAttributes(audioAttributes)
            .build()
        val soundIDs = mutableMapOf<Cue, Int>()
        val loaded = mutableSetOf<Int>()
        var outputClosed = false

        init {
            soundPool.setOnLoadCompleteListener { _, soundID, status ->
                synchronized(lock) {
                    if (!closed && !outputClosed && status == 0) loaded += soundID
                }
            }
            for (cue in Cue.entries) {
                soundIDs[cue] = soundPool.load(appContext, cue.resource, 1)
            }
        }

        override fun close() {
            synchronized(lock) {
                if (outputClosed) return
                outputClosed = true
            }
            soundPool.release()
        }
    }

    private val lock = Any()
    private val preferences = appContext.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val audioManager = appContext.getSystemService(AudioManager::class.java)
    private val notificationOutput = Output(AudioAttributes.USAGE_NOTIFICATION_EVENT)
    private val dndOverrideOutput = Output(AudioAttributes.USAGE_MEDIA)
    private val playbackExecutor: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()
    private var nextPlaybackAt = 0L
    private var closed = false

    fun play(event: AgentEvent) {
        val cue = cueFor(event) ?: return
        play(cue, event.id)
    }

    internal fun play(cue: Cue, eventID: Long) {
        val output = if (preferences.getBoolean(MainActivity.KEY_DND_OVERRIDE, false)) {
            dndOverrideOutput
        } else {
            notificationOutput
        }
        val delay = synchronized(lock) {
            if (closed) return
            val now = SystemClock.elapsedRealtime()
            val scheduledAt = maxOf(now, nextPlaybackAt)
            nextPlaybackAt = scheduledAt + cue.durationMillis + CUE_GAP_MILLIS
            scheduledAt - now
        }
        playbackExecutor.schedule(
            { playWhenLoaded(output, cue, eventID, LOAD_RETRIES) },
            delay,
            TimeUnit.MILLISECONDS,
        )
    }

    private fun playWhenLoaded(output: Output, cue: Cue, eventID: Long, retriesLeft: Int) {
        val soundID = synchronized(lock) {
            if (closed || output.outputClosed) return
            output.soundIDs[cue]?.takeIf { it in output.loaded }
        }
        if (soundID == null) {
            if (retriesLeft > 0) {
                playbackExecutor.schedule(
                    { playWhenLoaded(output, cue, eventID, retriesLeft - 1) },
                    LOAD_RETRY_MILLIS,
                    TimeUnit.MILLISECONDS,
                )
            } else {
                Log.w(TAG, "Audio cue ${cue.name} was not loaded for event $eventID")
            }
            return
        }
        val volume = (
            preferences.getInt(MainActivity.KEY_AUDIO_VOLUME, MainActivity.DEFAULT_AUDIO_VOLUME)
                .coerceIn(0, 100) / 100f
        )
        if (volume == 0f) return
        val focus = audioManager.requestAudioFocus(output.audioFocusRequest)
        if (focus != AudioManager.AUDIOFOCUS_REQUEST_GRANTED) {
            Log.w(TAG, "Audio focus was not granted for ${cue.name}, event $eventID")
        }
        val streamID = output.soundPool.play(soundID, volume, volume, 1, 0, 1f)
        if (streamID == 0) {
            audioManager.abandonAudioFocusRequest(output.audioFocusRequest)
            Log.w(TAG, "Audio cue ${cue.name} failed to start for event $eventID")
        } else {
            Log.i(TAG, "Playing audio cue ${cue.name} for event $eventID")
            playbackExecutor.schedule(
                { audioManager.abandonAudioFocusRequest(output.audioFocusRequest) },
                cue.durationMillis + FOCUS_RELEASE_DELAY_MILLIS,
                TimeUnit.MILLISECONDS,
            )
        }
    }

    internal fun cueFor(event: AgentEvent): Cue? {
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
        notificationOutput.close()
        dndOverrideOutput.close()
    }

    companion object {
        private const val CUE_GAP_MILLIS = 150L
        private const val LOAD_RETRY_MILLIS = 50L
        private const val LOAD_RETRIES = 20
        private const val FOCUS_RELEASE_DELAY_MILLIS = 50L
        private const val TAG = "MigiAudio"
    }
}
