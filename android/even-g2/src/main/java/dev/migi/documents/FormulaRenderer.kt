package dev.migi.documents

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import com.faceclaw.app.BmpUtil
import ru.noties.jlatexmath.JLatexMathAndroid
import ru.noties.jlatexmath.JLatexMathDrawable

object FormulaRenderer {
    data class Formula(val bitmap: Bitmap, val bmp: ByteArray)
    // JLaTeXMath has global font/parser state; serialize both phone and glasses rendering.
    @Synchronized fun render(context: Context, latex: String): Formula {
        require(latex.length <= 2000)
        // Bound expansion, nesting and dimensions before allowing allocation.
        require(!Regex("\\\\(newcommand|renewcommand|def|input|include|includegraphics|loop|while|rule|hspace|vspace|kern|phantom|resizebox|scalebox)\\b").containsMatchIn(latex)) { "Команда LaTeX не поддерживается" }
        var depth = 0
        for (c in latex) { if (c == '{') depth++; if (c == '}') depth--; require(depth in 0..24) { "Слишком сложная формула" } }
        require(depth == 0)
        JLatexMathAndroid.init(context.applicationContext)
        val drawable = JLatexMathDrawable.builder(latex).textSize(28f).color(Color.WHITE).background(Color.BLACK).padding(4).build()
        val w = drawable.intrinsicWidth; val h = drawable.intrinsicHeight
        require(w in 1..2048 && h in 1..1024) { "Формула слишком большая" }
        val scale = minOf(1f, 288f / w, 144f / h)
        require(scale >= 0.65f) { "Разбейте длинную формулу на несколько блоков" }
        val width = maxOf(20, kotlin.math.ceil(w * scale).toInt()).let { (it + 3) / 4 * 4 }.coerceAtMost(288)
        val height = maxOf(20, kotlin.math.ceil(h * scale).toInt()).coerceAtMost(144)
        val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        try {
            val canvas = Canvas(bitmap); canvas.drawColor(Color.BLACK)
            canvas.scale(scale, scale)
            drawable.setBounds(0, 0, w, h); drawable.draw(canvas)
            val pixels = IntArray(width * height)
            bitmap.getPixels(pixels, 0, width, 0, 0, width, height)
            val packed = ByteArray((pixels.size + 1) / 2)
            pixels.forEachIndexed { i, color -> packed[i / 2] = (packed[i / 2].toInt() or ((Color.red(color) shr 4) shl if (i % 2 == 0) 4 else 0)).toByte() }
            return Formula(bitmap, BmpUtil.build4bppBmpFromPacked(packed, width, height))
        } catch (e: Exception) { bitmap.recycle(); throw e }
    }
}
