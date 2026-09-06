package dev.migi.app

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import java.io.File
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/** Only explicit pending markers are sent; old local microphone tests are never auto-uploaded. */
class G2VoiceUploader(private val context: Context) : AutoCloseable {
    private val directory = File(context.getExternalFilesDir(null) ?: context.filesDir, "voice")
    private val executor = Executors.newSingleThreadExecutor()
    private val handler = Handler(Looper.getMainLooper())
    private val running = AtomicBoolean(false)
    @Volatile private var closed = false
    private val retry = object : Runnable {
        override fun run() { flush(); if (!closed) handler.postDelayed(this, 15_000) }
    }
    init { handler.post(retry) }
    fun enqueue(file: File) {
        check(file.parentFile?.canonicalFile == directory.canonicalFile && file.extension == "wav")
        File(directory, file.name + ".pending").outputStream().use { it.write(1); it.fd.sync() }
        flush()
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
                        val sent = FileExchangeClient(context).uploadVoice(wav)
                        File(directory, wav.name + ".sent").outputStream().use { it.write(sent.id.toByteArray()); it.fd.sync() }
                        check(marker.delete()) { "Could not clear voice queue marker" }
                        Log.i("MigiVoice", "Uploaded ${wav.name}: file=${sent.id}")
                    } catch (e: Exception) {
                        Log.w("MigiVoice", "Voice upload deferred: ${e.message}")
                        break
                    }
                }
            } finally { running.set(false) }
        }
    }
    override fun close() { closed = true; handler.removeCallbacks(retry); executor.shutdown() }
}
