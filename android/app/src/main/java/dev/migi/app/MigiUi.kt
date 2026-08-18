package dev.migi.app

import android.content.Context
import android.content.res.ColorStateList
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.LinearGradient
import android.graphics.Matrix
import android.graphics.Paint
import android.graphics.Path
import android.graphics.RadialGradient
import android.graphics.RectF
import android.graphics.Shader
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.util.AttributeSet
import android.view.View
import android.widget.TextView
import com.google.android.material.card.MaterialCardView
import java.io.File
import kotlin.math.max

internal object MigiPalette {
	val background = Color.rgb(9, 13, 21)
	val surface = Color.rgb(17, 23, 34)
	val surfaceHigh = Color.rgb(25, 34, 49)
	val surfaceBright = Color.rgb(34, 46, 64)
	val primary = Color.rgb(130, 155, 255)
	val onPrimary = Color.rgb(7, 16, 47)
	val secondary = Color.rgb(108, 229, 163)
	val text = Color.rgb(242, 245, 252)
	val muted = Color.rgb(152, 165, 186)
	val outline = Color.rgb(42, 53, 72)
	val danger = Color.rgb(255, 143, 156)
}

internal fun Context.dp(value: Int): Int = (value * resources.displayMetrics.density + 0.5f).toInt()

internal fun roundedDrawable(
	color: Int,
	radius: Float,
	strokeColor: Int = Color.TRANSPARENT,
	strokeWidth: Int = 0,
): GradientDrawable = GradientDrawable().apply {
	shape = GradientDrawable.RECTANGLE
	setColor(color)
	cornerRadius = radius
	if (strokeWidth > 0) setStroke(strokeWidth, strokeColor)
}

internal fun rippleDrawable(context: Context, color: Int, radiusDp: Int): RippleDrawable {
	val radius = context.dp(radiusDp).toFloat()
	val content = roundedDrawable(color, radius)
	val mask = roundedDrawable(Color.WHITE, radius)
	return RippleDrawable(ColorStateList.valueOf(0x2FFFFFFF), content, mask)
}

internal fun MaterialCardView.applyMigiCard(
	color: Int = MigiPalette.surface,
	radiusDp: Int = 24,
	stroke: Boolean = true,
) {
	cardElevation = 0f
	radius = context.dp(radiusDp).toFloat()
	setCardBackgroundColor(color)
	strokeWidth = if (stroke) context.dp(1) else 0
	strokeColor = MigiPalette.outline
	isClickable = false
}

internal fun TextView.applyMigiText(
	sizeSp: Float,
	color: Int = MigiPalette.text,
	weight: Int = Typeface.NORMAL,
) {
	textSize = sizeSp
	setTextColor(color)
	typeface = Typeface.create("sans-serif", weight)
	includeFontPadding = false
}

internal fun decodeArtwork(file: File, maximumSide: Int = 1_200): Bitmap? {
	val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
	BitmapFactory.decodeFile(file.absolutePath, bounds)
	if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
	var sample = 1
	while (max(bounds.outWidth / sample, bounds.outHeight / sample) > maximumSide * 2) {
		sample *= 2
	}
	return BitmapFactory.decodeFile(
		file.absolutePath,
		BitmapFactory.Options().apply {
			inSampleSize = sample
			inPreferredConfig = Bitmap.Config.ARGB_8888
		},
	)
}

/** Square artwork surface with a deterministic Migi illustration as its fallback. */
internal class PlaylistArtworkView @JvmOverloads constructor(
	context: Context,
	attributes: AttributeSet? = null,
) : View(context, attributes) {
	private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
	private val eye = Path()
	private val destination = RectF()
	private val clip = Path()
	private val matrix = Matrix()
	private var seed = "migi"
	private var artwork: Bitmap? = null
	var cornerRadius = context.dp(28).toFloat()

	init {
		importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
	}

	fun showFallback(value: String) {
		seed = value.ifBlank { "migi" }
		artwork = null
		invalidate()
	}

	fun showArtwork(value: String, bitmap: Bitmap) {
		seed = value.ifBlank { "migi" }
		artwork = bitmap
		alpha = 0.72f
		animate().alpha(1f).setDuration(240).start()
		invalidate()
	}

	override fun onDraw(canvas: Canvas) {
		super.onDraw(canvas)
		if (width == 0 || height == 0) return
		destination.set(0f, 0f, width.toFloat(), height.toFloat())
		clip.reset()
		clip.addRoundRect(destination, cornerRadius, cornerRadius, Path.Direction.CW)
		canvas.save()
		canvas.clipPath(clip)
		val bitmap = artwork
		if (bitmap == null) drawFallback(canvas) else drawBitmap(canvas, bitmap)
		canvas.restore()
	}

	private fun drawFallback(canvas: Canvas) {
		val hue = ((seed.hashCode().toLong() and 0xffffffffL) % 360L).toFloat()
		val first = Color.HSVToColor(floatArrayOf(hue, 0.64f, 0.88f))
		val second = Color.HSVToColor(floatArrayOf((hue + 58f) % 360f, 0.72f, 0.48f))
		val third = Color.HSVToColor(floatArrayOf((hue + 198f) % 360f, 0.56f, 0.26f))
		paint.shader = LinearGradient(
			0f,
			0f,
			width.toFloat(),
			height.toFloat(),
			intArrayOf(first, second, third),
			floatArrayOf(0f, 0.58f, 1f),
			Shader.TileMode.CLAMP,
		)
		canvas.drawRect(destination, paint)

		paint.shader = RadialGradient(
			width * 0.2f,
			height * 0.16f,
			width * 0.72f,
			intArrayOf(0xA0FFFFFF.toInt(), 0x18FFFFFF, Color.TRANSPARENT),
			null,
			Shader.TileMode.CLAMP,
		)
		canvas.drawCircle(width * 0.2f, height * 0.16f, width * 0.72f, paint)

		paint.shader = null
		paint.style = Paint.Style.STROKE
		paint.strokeWidth = context.dp(2).toFloat()
		paint.color = 0x38FFFFFF
		for (index in 0 until 4) {
			val inset = width * (0.08f + index * 0.075f)
			canvas.drawCircle(width * 0.78f, height * 0.78f, width * 0.44f - inset / 2f, paint)
		}

		val cx = width / 2f
		val cy = height / 2f
		val eyeWidth = width * 0.46f
		val eyeHeight = height * 0.2f
		eye.reset()
		eye.moveTo(cx - eyeWidth / 2f, cy)
		eye.cubicTo(cx - eyeWidth * 0.23f, cy - eyeHeight, cx + eyeWidth * 0.23f, cy - eyeHeight, cx + eyeWidth / 2f, cy)
		eye.cubicTo(cx + eyeWidth * 0.23f, cy + eyeHeight, cx - eyeWidth * 0.23f, cy + eyeHeight, cx - eyeWidth / 2f, cy)
		paint.color = 0xE8FFFFFF.toInt()
		paint.strokeWidth = context.dp(3).toFloat()
		canvas.drawPath(eye, paint)
		paint.style = Paint.Style.FILL
		paint.color = 0xD9080D17.toInt()
		canvas.drawCircle(cx, cy, width * 0.075f, paint)
		paint.color = Color.WHITE
		canvas.drawCircle(cx + width * 0.018f, cy - width * 0.02f, width * 0.018f, paint)
	}

	private fun drawBitmap(canvas: Canvas, bitmap: Bitmap) {
		val scale = max(width.toFloat() / bitmap.width, height.toFloat() / bitmap.height)
		val x = (width - bitmap.width * scale) / 2f
		val y = (height - bitmap.height * scale) / 2f
		matrix.reset()
		matrix.postScale(scale, scale)
		matrix.postTranslate(x, y)
		paint.shader = null
		paint.style = Paint.Style.FILL
		paint.alpha = 255
		canvas.drawBitmap(bitmap, matrix, paint)
		paint.shader = LinearGradient(
			0f,
			height * 0.55f,
			0f,
			height.toFloat(),
			Color.TRANSPARENT,
			0x70070B12,
			Shader.TileMode.CLAMP,
		)
		canvas.drawRect(destination, paint)
		paint.shader = null
	}
}
