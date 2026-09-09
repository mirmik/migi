package dev.migi.app

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import dev.migi.g2.PagerContent
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/** Durable private upload -> automatic dispatch with transcript and run-specific stop. */
class G2VoiceUploader(private val context: Context) : AutoCloseable {
    private val directory = File(context.getExternalFilesDir(null) ?: context.filesDir, "voice")
    private val executor = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())
    private val running = AtomicBoolean(false)
    @Volatile private var closed = false
    @Volatile private var review: PagerContent? = null
    @Volatile private var reviewMarker: File? = null
    @Volatile private var deciding = false
    private var transcriptShownAt = 0L
    private var transcriptId = ""
    var onChanged: (() -> Unit)? = null
    fun current(): PagerContent? = review
    private fun changed(value: PagerContent?) {
        if (review == value) return
        review = value
        handler.post { if (!closed) onChanged?.invoke() }
    }
    private fun persist(file: File, value: String) {
        val temporary = File(file.path + ".new")
        temporary.outputStream().use { it.write(value.toByteArray()); it.fd.sync() }
        check(temporary.renameTo(file)) { "Cannot save voice state" }
    }
    private val retry = object : Runnable {
        override fun run() { flush(); if (!closed) handler.postDelayed(this, 2_000) }
    }
    init { handler.post(retry) }
    fun enqueue(file: File) {
        check(file.parentFile?.canonicalFile == directory.canonicalFile && file.extension == "wav")
        persist(File(directory, file.name + ".pending"), "1")
        flush()
    }
    fun decide(id: String, confirmed: Boolean) {
        val marker = reviewMarker ?: return
        if (review?.voiceReviewId != id || deciding) return
        deciding = true
        executor.execute {
            try {
                val stopping = review?.voiceCanStop == true
                persist(File(marker.path + ".decision"), if (stopping) "stop" else if (confirmed) "confirmed" else "cancelled")
                // Show a waiting state, not another actionable confirmation.
                changed(review?.copy(voiceReviewId = null, voiceCanStop = false, body = if (stopping) "Останавливаю агента…" else if (confirmed) "Отправляю подтверждение…" else "Отменяю запрос…"))
            } finally { deciding = false; flush() }
        }
    }
    private fun flush() {
        if (closed || !running.compareAndSet(false, true)) return
        executor.execute {
            try {
                val pending = directory.listFiles()?.filter { it.name.endsWith(".wav.pending") }?.sortedBy { it.lastModified() }.orEmpty()
                for (marker in pending) {
                    if (closed) break
                    val wav = File(directory, marker.name.removeSuffix(".pending"))
                    if (!wav.isFile) { marker.delete(); continue }
                    try {
                        val client = VoiceClient(context)
                        val uploaded = File(marker.path + ".uploaded")
                        val id = if (uploaded.exists()) uploaded.readText() else client.upload(wav).also { persist(uploaded, it) }
                        val decision = File(marker.path + ".decision")
                        val state = client.state(id, if (decision.exists()) decision.readText() else "")
                        when (state.getString("status")) {
                            "confirmed", "cancelled", "failed", "completed" -> {
                                val text = state.optString("transcript")
                                val terminal = state.getString("status")
                                // Even a very fast answer must leave enough time to see
                                // what STT heard. Agent execution is never delayed by this.
                                if (terminal == "completed" && text.isNotBlank()) {
                                    if (transcriptId != id) { transcriptId = id; transcriptShownAt = android.os.SystemClock.elapsedRealtime() }
                                    if (android.os.SystemClock.elapsedRealtime() - transcriptShownAt < 3_000) {
                                        changed(PagerContent(-id.take(15).toLong(16), "Распознано · ответ готов", text, voicePending = true))
                                        break
                                    }
                                }
                                val wasStop = decision.exists() && decision.readText() == "stop"
                                persist(File(directory, wav.name + ".sent"), id)
                                check(marker.delete())
                                uploaded.delete(); decision.delete()
                                reviewMarker = null
                                if (state.getString("status") == "cancelled") {
                                    val cancelled = PagerContent(-id.take(15).toLong(16), "Отменено",
                                        if (wasStop) "Выполнение остановлено." else "Отменено.\nСообщение не отправлено агенту.", voicePending = true)
                                    changed(cancelled)
                                    handler.postDelayed({ if (review === cancelled) changed(null) }, 2_000)
                                } else changed(null)
                                Log.i("MigiVoice", "Voice $id: ${state.getString("status")}")
                            }
                            "running", "dispatching", "stopping" -> {
                                reviewMarker = marker
                                val text = state.optString("transcript")
                                if (text.isNotBlank() && transcriptId != id) { transcriptId = id; transcriptShownAt = android.os.SystemClock.elapsedRealtime() }
                                val stopping = state.getString("status") == "stopping" || (decision.exists() && decision.readText() == "stop")
                                changed(PagerContent(-id.take(15).toLong(16), if (stopping) "Остановка" else "Распознано",
                                    if (stopping) "Останавливаю агента…\n$text" else text,
                                    voiceReviewId = if (stopping) null else id, voicePending = true, voiceCanStop = !stopping))
                                break
                            }
                            "awaiting_confirmation" -> {
                                reviewMarker = marker
                                changed(PagerContent(-id.take(15).toLong(16), "Проверьте сообщение",
                                    state.getString("transcript"), voiceReviewId = id, voicePending = true))
                                break // one review at a time; never choose for the user
                            }
                            else -> {
                                reviewMarker = marker
                                changed(PagerContent(-id.take(15).toLong(16), "Распознавание",
                                    "Распознаю речь…\nТекст отправится агенту автоматически.", voicePending = true))
                                break
                            }
                        }
                    } catch (e: Exception) {
                        Log.w("MigiVoice", "Voice request deferred: ${e.message}")
                        break
                    }
                }
            } finally { running.set(false) }
        }
    }
    override fun close() { closed = true; handler.removeCallbacks(retry); executor.shutdown() }
}
