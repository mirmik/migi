package dev.migi.app

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Bundle
import android.provider.Settings
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView

class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }

        val preferences = getSharedPreferences(PREFERENCES, MODE_PRIVATE)
        val endpoint = EditText(this).apply {
            hint = getString(R.string.endpoint_hint)
            setText(preferences.getString(KEY_ENDPOINT, ""))
            inputType = android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        val certificatePin = EditText(this).apply {
            hint = getString(R.string.certificate_pin_hint)
            setText(preferences.getString(KEY_CERTIFICATE_PIN, ""))
            inputType = android.text.InputType.TYPE_CLASS_TEXT
        }
        val status = TextView(this).apply {
            text = getString(
                if (ConnectionService.isRunning) R.string.service_running else R.string.service_stopped,
            )
            textSize = 16f
        }

        val start = Button(this).apply {
            text = getString(R.string.start_connection)
            setOnClickListener {
                val value = endpoint.text.toString().trim()
                if (!value.startsWith("https://")) {
                    endpoint.error = getString(R.string.endpoint_required)
                    return@setOnClickListener
                }
                val rawPin = certificatePin.text.toString().trim()
                val pin = rawPin.filterNot { it.isWhitespace() || it == ':' }
                if (
                    pin.length != 64 ||
                    pin.any { !it.isDigit() && it.lowercaseChar() !in 'a'..'f' } ||
                    rawPin.any { !it.isWhitespace() && it != ':' && !it.isDigit() && it.lowercaseChar() !in 'a'..'f' }
                ) {
                    certificatePin.error = getString(R.string.certificate_pin_required)
                    return@setOnClickListener
                }
                preferences.edit()
                    .putString(KEY_ENDPOINT, value.trimEnd('/'))
                    .putString(KEY_CERTIFICATE_PIN, pin.uppercase())
                    .apply()
                startForegroundService(Intent(this@MainActivity, ConnectionService::class.java))
                status.setText(R.string.service_starting)
            }
        }

        val stop = Button(this).apply {
            text = getString(R.string.stop_connection)
            setOnClickListener {
                stopService(Intent(this@MainActivity, ConnectionService::class.java))
                status.setText(R.string.service_stopped)
            }
        }

        val battery = Button(this).apply {
            text = getString(R.string.open_battery_settings)
            setOnClickListener {
                startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
            }
        }

        val padding = (20 * resources.displayMetrics.density).toInt()
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, padding, padding, padding)
            addView(TextView(this@MainActivity).apply {
                setText(R.string.server_title)
                textSize = 24f
            })
            addView(endpoint, matchWidth())
            addView(certificatePin, matchWidth())
            addView(start, matchWidth())
            addView(stop, matchWidth())
            addView(battery, matchWidth())
            addView(status, matchWidth())
        })
    }

    private fun matchWidth() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    )

    companion object {
        const val PREFERENCES = "migi"
        const val KEY_ENDPOINT = "endpoint"
        const val KEY_CERTIFICATE_PIN = "certificate_pin"
    }
}
