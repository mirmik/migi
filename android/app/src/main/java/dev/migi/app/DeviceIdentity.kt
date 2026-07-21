package dev.migi.app

import android.content.Context
import java.util.UUID

internal object DeviceIdentity {
    fun get(context: Context): String {
        val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
        return preferences.getString(KEY_DEVICE_ID, null) ?: UUID.randomUUID().toString().also {
            check(preferences.edit().putString(KEY_DEVICE_ID, it).commit()) {
                "Failed to persist device ID"
            }
        }
    }

    private const val KEY_DEVICE_ID = "device_id"
}
