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
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.SeekBar
import android.widget.TextView
import java.time.Instant
import kotlin.concurrent.thread
import org.json.JSONObject

class MainActivity : Activity() {
	private lateinit var preferences: SharedPreferences
    private lateinit var endpoint: EditText
    private lateinit var certificatePin: EditText
    private lateinit var pilotSignerPin: EditText
    private lateinit var status: TextView
	private lateinit var pagerMessage: TextView
	private lateinit var releaseList: LinearLayout
	private lateinit var fileList: LinearLayout
	private lateinit var batteryButton: Button
	private var pendingDownload: SharedFile? = null
	private val preferenceListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
		if (key == KEY_PAGER_MESSAGE) runOnUiThread(::refreshPagerMessage)
		if (key == KEY_FILES_GENERATION) runOnUiThread(::refreshFiles)
	}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        buildContentView()
        handlePairingIntent(intent)
        handleSharedFileIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handlePairingIntent(intent)
        handleSharedFileIntent(intent)
    }

		override fun onStart() {
			super.onStart()
			ReleaseInstaller.foregroundActivity = this
			preferences.registerOnSharedPreferenceChangeListener(preferenceListener)
			refreshPagerMessage()
			ReleaseInstaller(this).reconcileSessions()
			refreshReleases()
			refreshFiles()
		refreshBatteryOptimizationState()
	}

		override fun onStop() {
			if (ReleaseInstaller.foregroundActivity === this) {
				ReleaseInstaller.foregroundActivity = null
			}
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
        pilotSignerPin = EditText(this).apply {
            hint = getString(R.string.pilot_signer_hint)
            setText(preferences.getString(KEY_PILOT_SIGNER_SHA256, ""))
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
        val dndOverride = CheckBox(this).apply {
            text = getString(R.string.play_sounds_during_dnd)
            isChecked = preferences.getBoolean(KEY_DND_OVERRIDE, false)
            setOnCheckedChangeListener { _, enabled ->
                preferences.edit().putBoolean(KEY_DND_OVERRIDE, enabled).apply()
            }
        }
        val audioVolumeLabel = TextView(this).apply {
            textSize = 14f
        }
        val audioVolume = SeekBar(this).apply {
            max = 100
            progress = preferences.getInt(KEY_AUDIO_VOLUME, DEFAULT_AUDIO_VOLUME)
                .coerceIn(0, 100)
            audioVolumeLabel.text = getString(R.string.migi_volume, progress)
            setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
                override fun onProgressChanged(seekBar: SeekBar?, value: Int, fromUser: Boolean) {
                    audioVolumeLabel.text = getString(R.string.migi_volume, value)
                    if (fromUser) {
                        preferences.edit().putInt(KEY_AUDIO_VOLUME, value).apply()
                    }
                }

                override fun onStartTrackingTouch(seekBar: SeekBar?) = Unit

                override fun onStopTrackingTouch(seekBar: SeekBar?) = Unit
            })
        }

        val padding = (20 * resources.displayMetrics.density).toInt()
        val content = LinearLayout(this).apply {
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
            addView(TextView(this@MainActivity).apply {
                setText(R.string.files_title)
                textSize = 20f
            })
            addView(Button(this@MainActivity).apply {
                setText(R.string.choose_file)
                setOnClickListener {
                    startActivityForResult(
                        Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
                            addCategory(Intent.CATEGORY_OPENABLE)
                            type = "*/*"
                        },
                        REQUEST_CHOOSE_FILE,
                    )
                }
            }, matchWidth())
            fileList = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
            }
            addView(fileList, matchWidth())
            addView(TextView(this@MainActivity).apply {
                setText(R.string.releases_title)
                textSize = 20f
            })
            releaseList = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
            }
            addView(releaseList, matchWidth())
            addView(endpoint, matchWidth())
            addView(certificatePin, matchWidth())
            addView(pilotSignerPin, matchWidth())
            addView(start, matchWidth())
            addView(stop, matchWidth())
            addView(batteryButton, matchWidth())
            addView(dndOverride, matchWidth())
            addView(audioVolumeLabel, matchWidth())
            addView(audioVolume, matchWidth())
            addView(status, matchWidth())
        }
        setContentView(ScrollView(this).apply {
            addView(content, matchWidth())
        })
    }

	override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
		super.onActivityResult(requestCode, resultCode, data)
		if (resultCode != RESULT_OK) return
		when (requestCode) {
			REQUEST_CHOOSE_FILE -> data?.data?.let(::uploadSharedFile)
			REQUEST_SAVE_FILE -> {
				val file = pendingDownload
				.also { pendingDownload = null }
				?: return
				val destination = data?.data ?: return
				status.setText(R.string.file_downloading)
				thread(name = "migi-file-download") {
					val result = runCatching { FileExchangeClient(this).download(file, destination) }
					runOnUiThread {
						status.text = result.fold(
							onSuccess = { getString(R.string.file_downloaded, file.name) },
							onFailure = { getString(R.string.file_transfer_failed, it.message ?: "unknown error") },
						)
					}
				}
			}
		}
	}

	private fun refreshPagerMessage() {
		pagerMessage.text = preferences.getString(KEY_PAGER_MESSAGE, null)
			?.takeIf { it.isNotBlank() }
			?: getString(R.string.pager_empty)
	}

	private fun refreshFiles() {
		fileList.removeAllViews()
		fileList.addView(TextView(this).apply { setText(R.string.files_loading) })
		thread(name = "migi-file-list") {
			val result = runCatching { FileExchangeClient(this).list() }
			runOnUiThread {
				fileList.removeAllViews()
				result.onFailure {
					fileList.addView(TextView(this).apply {
						text = getString(R.string.files_failed, it.message ?: "unknown error")
					})
				}.onSuccess { files ->
					if (files.isEmpty()) {
						fileList.addView(TextView(this).apply { setText(R.string.files_empty) })
					}
					for (file in files) {
						val item = LinearLayout(this).apply {
							orientation = LinearLayout.VERTICAL
							setPadding(0, 8, 0, 12)
						}
						item.addView(TextView(this).apply {
							text = getString(
								R.string.file_summary,
								file.name,
								formatFileSize(file.size),
								file.source,
								file.expiresAt.toString(),
							)
							setTextIsSelectable(true)
						})
						item.addView(Button(this).apply {
							setText(R.string.save_file)
							setOnClickListener {
								pendingDownload = file
								startActivityForResult(
									Intent(Intent.ACTION_CREATE_DOCUMENT).apply {
										addCategory(Intent.CATEGORY_OPENABLE)
										type = file.mime
										putExtra(Intent.EXTRA_TITLE, file.name)
									},
									REQUEST_SAVE_FILE,
								)
							}
						})
						fileList.addView(item, matchWidth())
					}
				}
			}
		}
	}

	private fun handleSharedFileIntent(intent: Intent?) {
		if (intent?.action != Intent.ACTION_SEND) return
		val uri = intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java) ?: run {
			status.setText(R.string.file_share_invalid)
			return
		}
		setIntent(Intent(this, MainActivity::class.java))
		uploadSharedFile(uri)
	}

	private fun uploadSharedFile(uri: Uri) {
		status.setText(R.string.file_uploading)
		thread(name = "migi-file-upload") {
			val result = runCatching { FileExchangeClient(this).upload(uri) }
			runOnUiThread {
				status.text = result.fold(
					onSuccess = { getString(R.string.file_uploaded, it.name) },
					onFailure = { getString(R.string.file_transfer_failed, it.message ?: "unknown error") },
				)
				if (result.isSuccess) refreshFiles()
			}
		}
	}

	private fun formatFileSize(bytes: Long): String = when {
		bytes >= 1024 * 1024 -> "%.1f MiB".format(bytes.toDouble() / (1024 * 1024))
		bytes >= 1024 -> "%.1f KiB".format(bytes.toDouble() / 1024)
		else -> "$bytes B"
	}

	private fun refreshReleases() {
		releaseList.removeAllViews()
		val releases = ReleaseRepository(this).listReleases()
		if (releases.isEmpty()) {
			releaseList.addView(TextView(this).apply {
				setText(R.string.releases_empty)
			})
			return
		}
		for (release in releases) {
			val item = LinearLayout(this).apply {
				orientation = LinearLayout.VERTICAL
				setPadding(0, 8, 0, 12)
			}
			item.addView(TextView(this).apply {
				text = buildString {
					append(release.artifact.packageName)
					append(" ")
					append(release.artifact.versionName)
					append(" (")
					append(release.artifact.versionCode)
					append(") — ")
					append(release.state)
					release.error?.let { append("\n").append(it) }
				}
				setTextIsSelectable(true)
			})
			when (release.state) {
				ReleaseRepository.STATE_PENDING,
				ReleaseRepository.STATE_DISMISSED -> item.addView(Button(this).apply {
					setText(R.string.download_release)
					setOnClickListener {
						isEnabled = false
						ReleaseInstaller(this@MainActivity).download(release.artifact.id) { result ->
							runOnUiThread {
								status.text = result.fold(
									onSuccess = { getString(R.string.release_downloaded) },
									onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
								)
								refreshReleases()
							}
						}
					}
				})
				ReleaseRepository.STATE_DOWNLOADED,
				ReleaseRepository.STATE_INSTALLING,
				ReleaseRepository.STATE_FAILED -> item.addView(Button(this).apply {
					if (release.tempPath == null) {
						setText(R.string.download_release)
						setOnClickListener {
							isEnabled = false
							ReleaseInstaller(this@MainActivity).download(release.artifact.id) { result ->
								runOnUiThread {
									status.text = result.fold(
										onSuccess = { getString(R.string.release_downloaded) },
										onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
									)
									refreshReleases()
								}
							}
						}
						return@apply
					}
					setText(R.string.install_release)
					setOnClickListener {
						val installer = ReleaseInstaller(this@MainActivity)
						if (!packageManager.canRequestPackageInstalls()) {
							startActivity(installer.unknownSourcesSettingsIntent())
							status.setText(R.string.allow_unknown_source)
							return@setOnClickListener
						}
						isEnabled = false
						installer.install(this@MainActivity, release.artifact.id) { result ->
							status.text = result.fold(
								onSuccess = { getString(R.string.install_submitted) },
								onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
							)
							refreshReleases()
						}
					}
				})
			}
			releaseList.addView(item, matchWidth())
		}
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
        val pilotSigner = normalizePin(pilotSignerPin.text.toString())
        if (pilotSignerPin.text.isNotBlank() && pilotSigner == null) {
            pilotSignerPin.error = getString(R.string.pilot_signer_invalid)
            return
        }
        if (CredentialStore(this).load() == null) {
            status.setText(R.string.device_not_paired)
            return
        }
        getSharedPreferences(PREFERENCES, MODE_PRIVATE).edit()
            .putString(KEY_ENDPOINT, value)
	            .putString(KEY_CERTIFICATE_PIN, pin)
	            .putString(KEY_PILOT_SIGNER_SHA256, pilotSigner ?: "")
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
	                        .commit(),
	                ) { "Failed to save paired server" }
	                ReleaseRepository(this).resetForPairing()
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
		const val KEY_FILES_GENERATION = "files_generation"
		const val KEY_DND_OVERRIDE = "dnd_override"
			const val KEY_AUDIO_VOLUME = "audio_volume"
			const val KEY_PILOT_SIGNER_SHA256 = "pilot_signer_sha256"
			const val DEFAULT_AUDIO_VOLUME = 100
			private const val REQUEST_CHOOSE_FILE = 20
			private const val REQUEST_SAVE_FILE = 21

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
