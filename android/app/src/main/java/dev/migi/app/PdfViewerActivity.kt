package dev.migi.app

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.drawable.BitmapDrawable
import android.graphics.pdf.PdfRenderer
import android.os.Bundle
import android.os.ParcelFileDescriptor
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.widget.LinearLayout
import android.widget.TextView
import com.google.android.material.button.MaterialButton
import java.io.File
import java.util.concurrent.Executors

class PdfViewerActivity : Activity() {
    private val executor = Executors.newSingleThreadExecutor()
    private var temporary: File? = null
    private var pageIndex = 0
    private var pageCount = 0
    private var loading = false
    private var disposed = false
    private lateinit var image: ZoomImageView
    private lateinit var status: TextView
    private lateinit var counter: TextView
    private lateinit var previous: MaterialButton
    private lateinit var next: MaterialButton

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val name = intent.getStringExtra(EXTRA_NAME).orEmpty().take(255)
        title = name
        temporary = PdfViewerPolicy.resolveViewerFile(cacheDir, intent.getStringExtra(EXTRA_PATH))
        pageIndex = savedInstanceState?.getInt(STATE_PAGE, 0) ?: 0
        image = ZoomImageView(this).apply { contentDescription = name }
        status = TextView(this).apply { applyMigiText(16f) }
        counter = TextView(this).apply {
            applyMigiText(14f)
            gravity = Gravity.CENTER
        }
        previous = MaterialButton(this).apply {
            setText(R.string.pdf_viewer_previous)
            isAllCaps = false
            setOnClickListener { if (!loading && pageIndex > 0) renderPage(pageIndex - 1) }
        }
        next = MaterialButton(this).apply {
            setText(R.string.pdf_viewer_next)
            isAllCaps = false
            setOnClickListener { if (!loading && pageIndex + 1 < pageCount) renderPage(pageIndex + 1) }
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(MigiPalette.background)
            setPadding(dp(16), dp(12), dp(16), dp(12))
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout())
                view.setPadding(dp(16) + bars.left, dp(12) + bars.top, dp(16) + bars.right, dp(12) + bars.bottom)
                insets
            }
            addView(LinearLayout(this@PdfViewerActivity).apply {
                gravity = Gravity.CENTER_VERTICAL
                addView(TextView(this@PdfViewerActivity).apply {
                    text = name
                    applyMigiText(18f)
                    maxLines = 2
                    ellipsize = android.text.TextUtils.TruncateAt.END
                }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                addView(MaterialButton(this@PdfViewerActivity).apply {
                    setText(R.string.close_pdf_viewer)
                    isAllCaps = false
                    setOnClickListener { finish() }
                })
            })
            addView(status)
            addView(image, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
            addView(LinearLayout(this@PdfViewerActivity).apply {
                gravity = Gravity.CENTER_VERTICAL
                addView(previous)
                addView(counter, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                addView(next)
            })
            addView(TextView(this@PdfViewerActivity).apply {
                setText(R.string.image_viewer_hint)
                applyMigiText(13f, MigiPalette.muted)
            })
        })
        renderPage(pageIndex)
    }

    private fun updateNavigation() {
        previous.isEnabled = !loading && pageIndex > 0
        next.isEnabled = !loading && pageIndex + 1 < pageCount
        counter.text = if (pageCount > 0) getString(R.string.pdf_viewer_page, pageIndex + 1, pageCount) else ""
    }

    private fun renderPage(requestedPage: Int) {
        loading = true
        status.visibility = View.VISIBLE
        status.setText(R.string.pdf_viewer_loading)
        updateNavigation()
        val file = temporary
        val targetEdge = (resources.displayMetrics.run { maxOf(widthPixels, heightPixels) }.toLong() * 2)
            .coerceAtMost(4096).toInt()
        executor.execute {
            val result = runCatching {
                requireNotNull(file) { getString(R.string.pdf_viewer_invalid) }
                require(file.length() in 1..PdfViewerPolicy.MAX_PDF_BYTES) { getString(R.string.pdf_viewer_invalid) }
                // Scope the descriptor and renderer to one job so rotation/closing cannot race them.
                ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY).use { descriptor ->
                    PdfRenderer(descriptor).use { renderer ->
                        val count = renderer.pageCount
                        require(count > 0) { getString(R.string.pdf_viewer_invalid) }
                        val index = requestedPage.coerceIn(0, count - 1)
                        renderer.openPage(index).use { page ->
                            val (width, height) = PdfViewerPolicy.renderSize(page.width, page.height, targetEdge)
                            val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
                            try {
                                bitmap.eraseColor(Color.WHITE)
                                page.render(bitmap, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                                RenderedPage(bitmap, index, count)
                            } catch (error: Throwable) {
                                bitmap.recycle()
                                throw error
                            }
                        }
                    }
                }
            }
            runOnUiThread {
                if (disposed || isFinishing || isDestroyed) {
                    result.getOrNull()?.bitmap?.recycle()
                    return@runOnUiThread
                }
                loading = false
                result.onSuccess { page ->
                    pageIndex = page.index
                    pageCount = page.count
                    image.show(BitmapDrawable(resources, page.bitmap))
                    image.contentDescription = getString(R.string.pdf_viewer_page, pageIndex + 1, pageCount)
                    status.visibility = View.GONE
                }.onFailure {
                    val reason = when (it) {
                        is SecurityException -> getString(R.string.pdf_viewer_password)
                        else -> getString(R.string.pdf_viewer_invalid)
                    }
                    status.text = getString(R.string.pdf_viewer_failed, reason)
                }
                updateNavigation()
            }
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        outState.putInt(STATE_PAGE, pageIndex)
        super.onSaveInstanceState(outState)
    }

    override fun onDestroy() {
        disposed = true
        image.setImageDrawable(null)
        if (isFinishing) {
            val file = temporary
            // Delete only after any running render has released its descriptor.
            executor.execute { file?.delete() }
        }
        executor.shutdown()
        super.onDestroy()
    }

    private data class RenderedPage(val bitmap: Bitmap, val index: Int, val count: Int)

    companion object {
        private const val EXTRA_NAME = "name"
        private const val EXTRA_PATH = "path"
        private const val STATE_PAGE = "page"

        fun intent(context: Context, name: String, file: File): Intent =
            Intent(context, PdfViewerActivity::class.java)
                .putExtra(EXTRA_NAME, name)
                .putExtra(EXTRA_PATH, file.absolutePath)
    }
}
