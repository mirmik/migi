package dev.migi.app

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import java.time.Instant
import kotlin.concurrent.thread
import org.json.JSONObject

class MainActivity : Activity() {
	private lateinit var preferences: SharedPreferences
    private lateinit var endpoint: EditText
    private lateinit var certificatePin: EditText
    private lateinit var status: TextView
	private lateinit var pagerMessage: TextView
	private lateinit var batteryButton: Button
	private val preferenceListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
		if (key == KEY_PAGER_MESSAGE) runOnUiThread(::refreshPagerMessage)
	}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        buildContentView()
        handlePairingIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handlePairingIntent(intent)
    }

	override fun onStart() {
		super.onStart()
		preferences.registerOnSharedPreferenceChangeListener(preferenceListener)
		refreshPagerMessage()
		refreshBatteryOptimizationState()
	}

	override fun onStop() {
		preferences.unregisterOnSharedPreferenceChangeListener(preferenceListener)
		super.onStop()
	}

    private fun buildContentView() {
		preferences = getSharedPreferences(PREFERENCES, MODE_PRIVATE)
        endpoint = EditText(this).apply {
            hint = getString(R.string.endpoint_hint)
            setText(preferences.getString(KEY_ENDPOINT, ""))
            inputType = android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        certificatePin = EditText(this).apply {
            hint = getString(R.string.certificate_pin_hint)
            setText(preferences.getString(KEY_CERTIFICATE_PIN, ""))
            inputType = android.text.InputType.TYPE_CLASS_TEXT
        }
        status = TextView(this).apply {
            text = when {
                ConnectionService.isRunning -> getString(R.string.service_running)
                CredentialStore(this@MainActivity).load() == null -> getString(R.string.device_not_paired)
                else -> getString(R.string.service_stopped)
            }
            textSize = 16f
        }
		pagerMessage = TextView(this).apply {
			textSize = 20f
			setTextIsSelectable(true)
			setPadding(0, 6, 0, 18)
		}

        val start = Button(this).apply {
            text = getString(R.string.start_connection)
            setOnClickListener { startConfiguredConnection() }
        }
        val stop = Button(this).apply {
            text = getString(R.string.stop_connection)
            setOnClickListener {
                stopService(Intent(this@MainActivity, ConnectionService::class.java))
                status.setText(R.string.service_stopped)
            }
        }
        batteryButton = Button(this).apply {
            setOnClickListener {
				requestBatteryOptimizationExemption()
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
			addView(TextView(this@MainActivity).apply {
				setText(R.string.pager_title)
				textSize = 13f
			})
			addView(pagerMessage, matchWidth())
            addView(endpoint, matchWidth())
            addView(certificatePin, matchWidth())
            addView(start, matchWidth())
            addView(stop, matchWidth())
            addView(batteryButton, matchWidth())
            addView(status, matchWidth())
        })
    }

	private fun refreshPagerMessage() {
		pagerMessage.text = preferences.getString(KEY_PAGER_MESSAGE, null)
			?.takeIf { it.isNotBlank() }
			?: getString(R.string.pager_empty)
	}

	private fun refreshBatteryOptimizationState() {
		val exempt = getSystemService(PowerManager::class.java)
			.isIgnoringBatteryOptimizations(packageName)
		batteryButton.isEnabled = !exempt
		batteryButton.setText(
			if (exempt) R.string.battery_optimization_disabled
			else R.string.allow_reliable_background_delivery,
		)
	}

	private fun requestBatteryOptimizationExemption() {
		if (getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)) {
			refreshBatteryOptimizationState()
			return
		}
		startActivity(
			Intent(
				Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
				Uri.parse("package:$packageName"),
			),
		)
	}

    private fun startConfiguredConnection() {
        val value = endpoint.text.toString().trim().trimEnd('/')
        if (!value.startsWith("https://")) {
            endpoint.error = getString(R.string.endpoint_required)
            return
        }
        val pin = normalizePin(certificatePin.text.toString())
        if (pin == null) {
            certificatePin.error = getString(R.string.certificate_pin_required)
            return
        }
        if (CredentialStore(this).load() == null) {
            status.setText(R.string.device_not_paired)
            return
        }
        getSharedPreferences(PREFERENCES, MODE_PRIVATE).edit()
            .putString(KEY_ENDPOINT, value)
            .putString(KEY_CERTIFICATE_PIN, pin)
            .apply()
        startForegroundService(
            Intent(this, ConnectionService::class.java).setAction(ConnectionService.ACTION_RECONFIGURE),
        )
        status.setText(R.string.service_starting)
    }

    private fun handlePairingIntent(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        val invitation = PairingInvitation.parse(intent.data) ?: run {
            setIntent(Intent(this, MainActivity::class.java))
            status.setText(R.string.invalid_pairing_invitation)
            return
        }
        // The one-time secret must not be replayed after activity recreation.
        setIntent(Intent(this, MainActivity::class.java))
        AlertDialog.Builder(this)
            .setTitle(R.string.confirm_pairing_title)
            .setMessage(
                getString(
                    R.string.confirm_pairing_message,
                    invitation.endpoint,
                    invitation.pin.chunked(2).joinToString(":"),
                    invitation.expiresAt.toString(),
                ),
            )
            .setNegativeButton(android.R.string.cancel, null)
            .setPositiveButton(R.string.pair_device) { _, _ -> pair(invitation) }
            .show()
    }

    private fun pair(invitation: PairingInvitation) {
        status.setText(R.string.pairing_in_progress)
        thread(name = "migi-pair") {
            val result = runCatching {
                val response = NativeQuicClient.pair(
                    endpoint = invitation.endpoint,
                    certificatePin = invitation.pin,
                    secret = invitation.secret,
                    deviceID = DeviceIdentity.get(this),
                    deviceName = "${Build.MANUFACTURER} ${Build.MODEL}",
                )
                check(!response.startsWith("MIGI_ERROR:")) {
                    response.removePrefix("MIGI_ERROR:")
                }
                val json = JSONObject(response)
                check(json.getString("device_id") == DeviceIdentity.get(this)) {
                    "Server returned a different device ID"
                }
                CredentialStore(this).save(json.getString("token"))
                check(
                    getSharedPreferences(PREFERENCES, MODE_PRIVATE).edit()
                        .putString(KEY_ENDPOINT, invitation.endpoint)
                        .putString(KEY_CERTIFICATE_PIN, invitation.pin)
                        .remove(EventStreamClient.KEY_LAST_EVENT_ID)
                        .commit(),
                ) { "Failed to save paired server" }
            }
            runOnUiThread {
                result.onSuccess {
                    endpoint.setText(invitation.endpoint)
                    certificatePin.setText(invitation.pin)
                    startForegroundService(
                        Intent(this, ConnectionService::class.java)
                            .setAction(ConnectionService.ACTION_RECONFIGURE),
                    )
                    status.setText(R.string.pairing_complete)
					requestBatteryOptimizationExemption()
                }.onFailure {
                    status.text = getString(R.string.pairing_failed, it.message ?: "unknown error")
                }
            }
        }
    }

    private fun matchWidth() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    )

    private data class PairingInvitation(
        val endpoint: String,
        val pin: String,
        val secret: String,
        val expiresAt: Instant,
    ) {
        companion object {
            fun parse(uri: Uri?): PairingInvitation? = runCatching {
                require(uri?.scheme == "migi" && uri.host == "pair")
                val endpoint = requireNotNull(uri.getQueryParameter("endpoint")).trimEnd('/')
                require(endpoint.startsWith("https://"))
                val pin = requireNotNull(normalizePin(uri.getQueryParameter("pin")))
                val secret = requireNotNull(uri.getQueryParameter("secret"))
                require(secret.matches(Regex("^[A-Za-z0-9_-]{43}$")))
                val expires = Instant.parse(requireNotNull(uri.getQueryParameter("expires")))
                require(expires.isAfter(Instant.now()))
                PairingInvitation(endpoint, pin, secret, expires)
            }.getOrNull()
        }
    }

    companion object {
        const val PREFERENCES = "migi"
        const val KEY_ENDPOINT = "endpoint"
        const val KEY_CERTIFICATE_PIN = "certificate_pin"
		const val KEY_PAGER_MESSAGE = "pager_message"

        private fun normalizePin(raw: String?): String? {
            if (raw == null) return null
            val trimmed = raw.trim()
            val compact = trimmed.filterNot { it.isWhitespace() || it == ':' }
            return compact.uppercase().takeIf {
                it.length == 64 && it.all { character -> character.isHexDigit() } &&
                    trimmed.all { character -> character.isWhitespace() || character == ':' || character.isHexDigit() }
            }
        }

        private fun Char.isHexDigit(): Boolean = isDigit() || lowercaseChar() in 'a'..'f'
    }
}
