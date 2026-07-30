package dev.migi.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import kotlin.concurrent.thread

class PackageReplacedReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val application = context.applicationContext
        val pending = goAsync()
        thread(name = "migi-package-replaced") {
            try {
                recoverConnection(application, "package replacement")
            } finally {
                pending.finish()
            }
        }
    }
}

internal fun recoverConnection(context: Context, source: String) {
    val preferences = context.getSharedPreferences(
        MainActivity.PREFERENCES,
        Context.MODE_PRIVATE,
    )
    runCatching {
        ReleaseInstaller(context).reconcileSessions()
        val endpoint = preferences.getString(MainActivity.KEY_ENDPOINT, null)
        val pin = preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, null)
        val credential = CredentialStore(context).load()
        if (!hasStoredConnection(endpoint, pin, credential)) {
            preferences.edit().remove(MainActivity.KEY_CONNECTION_RECOVERY_ERROR).apply()
            Log.i("MigiRecovery", "$source while unpaired; connection restart skipped")
            return
        }
        context.startForegroundService(
            Intent(context, ConnectionService::class.java)
                .setAction(ConnectionService.ACTION_RECONFIGURE),
        )
        Log.i("MigiRecovery", "$source connection recovery submitted")
    }.onFailure { error ->
        Log.e("MigiRecovery", "$source connection recovery failed", error)
        check(
            preferences.edit()
                .putString(
                    MainActivity.KEY_CONNECTION_RECOVERY_ERROR,
                    error.message ?: error.javaClass.simpleName,
                )
                .commit(),
        ) {
            "Failed to persist connection recovery error"
        }
    }
}

internal fun hasStoredConnection(endpoint: String?, pin: String?, credential: String?): Boolean {
    val normalizedPin = pin
        ?.filterNot { it == ':' || it.isWhitespace() }
        ?.lowercase()
    return endpoint?.trim()?.let {
        it.startsWith("https://") && it.length > "https://".length
    } == true &&
        normalizedPin?.matches(Regex("[0-9a-f]{64}")) == true &&
        credential?.matches(Regex("[A-Za-z0-9_-]{43}")) == true
}
