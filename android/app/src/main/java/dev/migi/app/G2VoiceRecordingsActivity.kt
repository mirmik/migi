package dev.migi.app

import android.app.Activity
import android.media.MediaPlayer
import android.os.Bundle
import android.widget.*
import java.io.File
import java.text.DateFormat
import java.util.Date

/** Local inspection of microphone recordings, before an agent destination is configured. */
class G2VoiceRecordingsActivity : Activity() {
    private var player: MediaPlayer? = null
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars())
                view.setPadding(24 + bars.left, 24 + bars.top, 24 + bars.right, 24 + bars.bottom); insets
            }
        }
        fun label(value: String) = root.addView(TextView(this).apply { text = value; textSize = 18f; setPadding(8, 18, 8, 18) })
        label("Голосовые записи G2")
        label("Долгое нажатие на очках — начать. Дождитесь «Слушаю». Касание — отправить, двойное — отменить. Максимум 60 секунд. При включённом подключении к очкам новые записи отправляются на сервер для распознавания и ответа модели.")
        root.addView(Button(this).apply { text = "Остановить воспроизведение"; setOnClickListener { stopPlayback() } })
        root.addView(Button(this).apply { text = "Обновить список"; setOnClickListener { recreate() } })
        val directory = File(getExternalFilesDir(null) ?: filesDir, "voice")
        val files = directory.listFiles()?.filter { it.isFile && it.extension == "wav" }?.sortedByDescending { it.lastModified() }.orEmpty()
        if (files.isEmpty()) label("Записей пока нет")
        for (file in files) {
            label(when {
                File(directory, file.name + ".pending").exists() -> "Ожидает отправки"
                File(directory, file.name + ".sent").exists() -> "Передано серверу"
                else -> "Локальная запись"
            })
            root.addView(Button(this).apply {
                val seconds = ((file.length() - 44).coerceAtLeast(0) / 32000.0)
                text = "Слушать: ${DateFormat.getDateTimeInstance().format(Date(file.lastModified()))} · ${"%.1f".format(seconds)} с"
                setOnClickListener {
                    stopPlayback()
                    val next = MediaPlayer()
                    player = next
                    try {
                        next.setDataSource(file.absolutePath)
                        next.setOnPreparedListener { if (player === it) it.start() }
                        next.setOnCompletionListener { stopPlayback() }
                        next.setOnErrorListener { _, _, _ -> stopPlayback(); Toast.makeText(this@G2VoiceRecordingsActivity, "Не удалось воспроизвести запись", Toast.LENGTH_SHORT).show(); true }
                        next.prepareAsync()
                    } catch (e: Exception) { stopPlayback(); Toast.makeText(this@G2VoiceRecordingsActivity, e.message, Toast.LENGTH_SHORT).show() }
                }
            })
        }
        setContentView(ScrollView(this).apply { addView(root) })
    }
    private fun stopPlayback() { player?.release(); player = null }
    override fun onStop() { stopPlayback(); super.onStop() }
}
