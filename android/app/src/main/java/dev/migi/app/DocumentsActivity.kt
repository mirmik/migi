package dev.migi.app

import android.app.Activity
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.os.Bundle
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import com.faceclaw.app.NativePage
import dev.migi.documents.DocumentRenderer
import java.util.concurrent.Executors

/** Offline reader using the same prepared pages as the glasses. No WebView or remote resources. */
class DocumentsActivity : Activity() {
    private lateinit var root: LinearLayout
    private lateinit var repository: DocumentRepository
    private val executor = Executors.newSingleThreadExecutor()
    private var generation = 0
    private var eventId = 0L
    private var pages: List<NativePage> = emptyList()
    private var page = 0
    private var documentTitle = ""
    companion object { const val EXTRA_EVENT_ID = "document_event_id" }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        repository = DocumentRepository(this)
        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(20, 20, 20, 20)
            setOnApplyWindowInsetsListener { v, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars())
                v.setPadding(20 + bars.left, 20 + bars.top, 20 + bars.right, 20 + bars.bottom); insets
            }
        }
        setContentView(ScrollView(this).apply { addView(root) })
        val id = savedInstanceState?.getLong(EXTRA_EVENT_ID) ?: intent.getLongExtra(EXTRA_EVENT_ID, 0)
        if (id > 0) open(id) else list()
    }
    override fun onSaveInstanceState(outState: Bundle) { outState.putLong(EXTRA_EVENT_ID, eventId); super.onSaveInstanceState(outState) }
    override fun onDestroy() { generation++; executor.shutdownNow(); super.onDestroy() }
    private fun label(text: String, size: Float = 18f) = root.addView(TextView(this).apply { this.text = text; textSize = size; setPadding(8, 12, 8, 12) })
    private fun button(text: String, action: () -> Unit) = root.addView(Button(this).apply { this.text = text; setOnClickListener { action() } })
    private fun list() {
        generation++; eventId = 0; pages = emptyList(); root.removeAllViews()
        label("Документы", 26f)
        button("Вернуться к пейджеру на очках") { repository.returnToPager(); android.widget.Toast.makeText(this, "Пейджер выбран", android.widget.Toast.LENGTH_SHORT).show() }
        val entries = repository.entries()
        if (entries.isEmpty()) label("Здесь появятся записки, присланные агентами. Они доступны и без интернета.")
        entries.forEach { entry -> button("${entry.title}\n${entry.agent}") { open(entry.eventId) } }
    }
    private fun open(id: Long) {
        val content = repository.content(id) ?: return list()
        eventId = id; page = content.savedPage; documentTitle = content.title
        val request = ++generation
        root.removeAllViews(); label(documentTitle, 24f); label("Подготовка страниц…")
        executor.execute {
            val result = runCatching { DocumentRenderer.render(applicationContext, checkNotNull(content.document), id.toString()) }
            runOnUiThread {
                if (isDestroyed || request != generation) return@runOnUiThread
                result.onSuccess { pages = it; page = page.coerceIn(0, pages.lastIndex); display() }
                    .onFailure { root.removeAllViews(); label("Не удалось открыть документ: ${it.message}"); button("К списку", ::list) }
            }
        }
    }
    private fun display() {
        root.removeAllViews()
        button("Все документы", ::list)
        label(documentTitle, 24f)
        label("Страница ${page + 1} из ${pages.size}")
        root.addView(PageView(pages[page]), LinearLayout.LayoutParams(-1, -2))
        val navigation = LinearLayout(this)
        navigation.addView(Button(this).apply {
            text = "Назад"; isEnabled = page > 0
            setOnClickListener { page--; repository.savePage(eventId, page); display() }
        }, LinearLayout.LayoutParams(0, -2, 1f))
        navigation.addView(Button(this).apply {
            text = "Далее"; isEnabled = page < pages.lastIndex
            setOnClickListener { page++; repository.savePage(eventId, page); display() }
        }, LinearLayout.LayoutParams(0, -2, 1f))
        root.addView(navigation)
        button("Читать на очках") {
            repository.savePage(eventId, page)
            repository.select(eventId)
            if (G2PagerBridge.enabled(this)) {
                startForegroundService(Intent(this, ConnectionService::class.java))
                android.widget.Toast.makeText(this, "Документ отправлен на очки", android.widget.Toast.LENGTH_SHORT).show()
            } else {
                android.widget.Toast.makeText(this, "Включите вывод на очки", android.widget.Toast.LENGTH_SHORT).show()
                startActivity(Intent(this, G2ExperimentActivity::class.java))
            }
        }
    }
    private inner class PageView(private val snapshot: NativePage) : View(this@DocumentsActivity) {
        private val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = Color.WHITE; textSize = 24f }
        private val images = snapshot.images.map { image ->
            val data = image.bmp
            val offset = java.nio.ByteBuffer.wrap(data).order(java.nio.ByteOrder.LITTLE_ENDIAN).getInt(10)
            val stride = ((image.width + 1) / 2 + 3) and -4
            val pixels = IntArray(image.width * image.height) { index ->
                val x = index % image.width; val y = index / image.width
                val value = data[offset + (image.height - 1 - y) * stride + x / 2].toInt() and 255
                val gray = (if (x % 2 == 0) value shr 4 else value and 15) * 17
                Color.rgb(gray, gray, gray)
            }
            Bitmap.createBitmap(pixels, image.width, image.height, Bitmap.Config.ARGB_8888)
        }
        override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
            val width = MeasureSpec.getSize(widthMeasureSpec)
            setMeasuredDimension(width, width / 2)
        }
        override fun onDraw(canvas: Canvas) {
            canvas.drawColor(Color.BLACK)
            canvas.save(); canvas.scale(width / 576f, height / 288f)
            snapshot.texts.forEach { text ->
                canvas.save(); canvas.clipRect(text.x, text.y, text.x + text.width, text.y + text.height)
                text.text.split('\n').forEachIndexed { i, line -> canvas.drawText(line, text.x.toFloat(), text.y + 23f + i * 27, paint) }
                canvas.restore()
            }
            snapshot.images.forEachIndexed { i, image -> canvas.drawBitmap(images[i], image.x.toFloat(), image.y.toFloat(), paint) }
            canvas.restore()
        }
        override fun onDetachedFromWindow() { images.forEach { it.recycle() }; super.onDetachedFromWindow() }
    }
}
