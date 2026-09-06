package dev.migi.g2

import android.content.Context

/** Shared by the reader and the service; an unset brightness preserves the glasses' setting. */
object G2DisplaySettings {
    const val PREFS = "g2-experiment"
    const val BRIGHTNESS_MODE = "display_brightness_mode"
    const val BRIGHTNESS_LEVEL = "display_brightness_level"
    const val INVERT_SCROLL = "invert_scroll"
    fun invertScroll(context: Context) = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        .getBoolean(INVERT_SCROLL, false)
    fun applyBrightness(context: Context, connection: com.faceclaw.app.FaceclawBleCommunicator) {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        when (prefs.getString(BRIGHTNESS_MODE, "device")) {
            "auto" -> connection.setBrightness(true, 50)
            "manual" -> connection.setBrightness(false, prefs.getInt(BRIGHTNESS_LEVEL, 50).coerceIn(1, 100))
        }
    }
}
