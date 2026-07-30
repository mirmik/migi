package dev.migi.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlin.concurrent.thread

class BootCompletedReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val pending = goAsync()
        thread(name = "migi-boot-completed") {
            try {
                recoverConnection(context.applicationContext, "device boot")
            } finally {
                pending.finish()
            }
        }
    }
}
