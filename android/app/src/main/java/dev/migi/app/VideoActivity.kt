package dev.migi.app

import android.app.Activity
import android.content.Intent
import android.os.Bundle

/** Compatibility entry point for notifications created before video became a tab. */
class VideoActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        startActivity(Intent(this, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
            .putExtra(MainActivity.EXTRA_OPEN_TAB, MainActivity.TAB_VIDEO))
        finish()
    }
}
