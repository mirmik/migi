package dev.migi.app

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.graphics.ImageDecoder
import android.graphics.Matrix
import android.graphics.drawable.AnimatedImageDrawable
import android.graphics.drawable.Drawable
import android.os.Bundle
import android.view.GestureDetector
import android.view.MotionEvent
import android.view.ScaleGestureDetector
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import com.google.android.material.button.MaterialButton
import java.io.File
import kotlin.concurrent.thread

class ImageViewerActivity : Activity() {
    private var temporary: File? = null
    private var animation: AnimatedImageDrawable? = null
    private var started = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val name = intent.getStringExtra(EXTRA_NAME).orEmpty().take(255)
        title = name
        temporary = ImageViewerPolicy.resolveViewerFile(cacheDir, intent.getStringExtra(EXTRA_PATH))
        val image = ZoomImageView(this).apply { contentDescription = name }
        val status = TextView(this).apply {
            setText(R.string.image_viewer_loading)
            applyMigiText(16f)
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(MigiPalette.background)
            setPadding(dp(16), dp(12), dp(16), dp(12))
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars() or android.view.WindowInsets.Type.displayCutout())
                view.setPadding(dp(16) + bars.left, dp(12) + bars.top, dp(16) + bars.right, dp(12) + bars.bottom)
                insets
            }
            addView(LinearLayout(this@ImageViewerActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = android.view.Gravity.CENTER_VERTICAL
                addView(TextView(this@ImageViewerActivity).apply {
                    text = name
                    applyMigiText(18f)
                    maxLines = 2
                    ellipsize = android.text.TextUtils.TruncateAt.END
                }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                addView(MaterialButton(this@ImageViewerActivity).apply {
                    setText(R.string.close_image_viewer)
                    isAllCaps = false
                    setOnClickListener { finish() }
                })
            })
            addView(status)
            addView(image, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
            addView(TextView(this@ImageViewerActivity).apply {
                setText(R.string.image_viewer_hint)
                applyMigiText(13f, MigiPalette.muted)
            })
        })
        val file = temporary
        thread(name = "migi-image-decode") {
            val result = runCatching {
                requireNotNull(file) { "Invalid viewer file" }
                require(file.length() in 1..ImageViewerPolicy.MAX_IMAGE_BYTES) { "Image exceeds viewer limit" }
                ImageDecoder.decodeDrawable(ImageDecoder.createSource(file)) { decoder, info, _ ->
                    val (width, height) = ImageViewerPolicy.decodeSize(info.size.width, info.size.height)
                    decoder.setTargetSize(width, height)
                }
            }
            runOnUiThread {
                if (isDestroyed || isFinishing) {
                    (result.getOrNull() as? AnimatedImageDrawable)?.stop()
                    if (isFinishing) file?.delete()
                    return@runOnUiThread
                }
                result.onSuccess {
                    status.visibility = View.GONE
                    image.show(it)
                    animation = it as? AnimatedImageDrawable
                    if (started) animation?.start()
                }.onFailure {
                    status.text = getString(R.string.image_viewer_failed, it.message ?: it.javaClass.simpleName)
                }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        started = true
        animation?.start()
    }

    override fun onStop() {
        started = false
        animation?.stop()
        super.onStop()
    }

    override fun onDestroy() {
        animation?.stop()
        animation = null
        if (isFinishing) temporary?.delete()
        super.onDestroy()
    }

    companion object {
        private const val EXTRA_NAME = "name"
        private const val EXTRA_PATH = "path"

        fun intent(context: Context, name: String, file: File): Intent =
            Intent(context, ImageViewerActivity::class.java)
                .putExtra(EXTRA_NAME, name)
                .putExtra(EXTRA_PATH, file.absolutePath)
    }
}

private class ZoomImageView(context: Context) : ImageView(context) {
    private var fit = 1f
    private var zoom = 1f
    private var offsetX = 0f
    private var offsetY = 0f
    private val transform = Matrix()
    private val pinch = ScaleGestureDetector(context, object : ScaleGestureDetector.SimpleOnScaleGestureListener() {
        override fun onScale(detector: ScaleGestureDetector): Boolean {
            zoomAt((zoom * detector.scaleFactor).coerceIn(1f, 8f), detector.focusX, detector.focusY)
            return true
        }
    })
    private val gestures = GestureDetector(context, object : GestureDetector.SimpleOnGestureListener() {
        override fun onDown(e: MotionEvent): Boolean = true
        override fun onScroll(e1: MotionEvent?, e2: MotionEvent, distanceX: Float, distanceY: Float): Boolean {
            if (!pinch.isInProgress) {
                offsetX -= distanceX
                offsetY -= distanceY
                updateTransform()
            }
            return true
        }
        override fun onDoubleTap(e: MotionEvent): Boolean {
            zoomAt(if (zoom > 1f) 1f else 3f, e.x, e.y)
            return true
        }
        override fun onSingleTapUp(e: MotionEvent): Boolean = performClick()
    })

    init { scaleType = ScaleType.MATRIX }

    fun show(image: Drawable) {
        setImageDrawable(image)
        reset()
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        reset()
    }

    private fun reset() {
        val image = drawable ?: return
        if (width <= 0 || height <= 0) return
        fit = minOf(width.toFloat() / image.intrinsicWidth, height.toFloat() / image.intrinsicHeight)
        zoom = 1f
        offsetX = 0f
        offsetY = 0f
        updateTransform()
    }

    private fun zoomAt(value: Float, x: Float, y: Float) {
        val factor = value / zoom
        offsetX = x - (x - offsetX) * factor
        offsetY = y - (y - offsetY) * factor
        zoom = value
        updateTransform()
    }

    private fun updateTransform() {
        val image = drawable ?: return
        val scale = fit * zoom
        val w = image.intrinsicWidth * scale
        val h = image.intrinsicHeight * scale
        offsetX = if (w <= width) (width - w) / 2 else offsetX.coerceIn(width - w, 0f)
        offsetY = if (h <= height) (height - h) / 2 else offsetY.coerceIn(height - h, 0f)
        transform.setScale(scale, scale)
        transform.postTranslate(offsetX, offsetY)
        imageMatrix = transform
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        pinch.onTouchEvent(event)
        gestures.onTouchEvent(event)
        return true
    }

    override fun performClick(): Boolean {
        super.performClick()
        return true
    }
}
