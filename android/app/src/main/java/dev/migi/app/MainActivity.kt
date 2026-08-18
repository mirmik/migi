package dev.migi.app

import android.Manifest
import android.app.Activity
import android.content.ComponentName
import android.content.Intent
import android.content.SharedPreferences
import android.content.res.ColorStateList
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.MediaStore
import android.provider.Settings
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.ImageButton
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.SeekBar
import android.widget.TextView
import android.widget.Toast
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.media3.common.C
import androidx.media3.common.Player
import androidx.media3.session.MediaController
import androidx.media3.session.SessionToken
import com.google.common.util.concurrent.ListenableFuture
import com.google.android.material.bottomnavigation.BottomNavigationView
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import com.google.android.material.slider.Slider
import com.google.android.material.switchmaterial.SwitchMaterial
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import java.time.Instant
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicLong
import kotlin.concurrent.thread
import org.json.JSONObject

class MainActivity : Activity() {
    private lateinit var preferences: SharedPreferences
    private lateinit var endpoint: EditText
    private lateinit var certificatePin: EditText
    private lateinit var status: TextView
    private lateinit var settingsStatus: TextView
    private lateinit var pagerMessage: TextView
    private lateinit var releaseList: LinearLayout
    private lateinit var fileList: LinearLayout
    private lateinit var playbackSummary: TextView
    private lateinit var playbackTracks: LinearLayout
    private lateinit var playbackStatus: TextView
    private lateinit var playbackButton: Button
    private lateinit var playbackCurrent: TextView
    private lateinit var playbackArtist: TextView
    private lateinit var playbackHeroLabel: TextView
    private lateinit var playbackPosition: TextView
    private lateinit var playbackSeek: Slider
    private lateinit var playbackPlayPause: ImageButton
    private lateinit var playbackPrevious: ImageButton
    private lateinit var playbackNext: ImageButton
    private lateinit var playbackShuffle: ImageButton
    private lateinit var playbackRepeat: ImageButton
    private lateinit var playbackStopButton: Button
    private lateinit var playbackArtwork: PlaylistArtworkView
    private lateinit var miniArtwork: PlaylistArtworkView
    private lateinit var miniPlayer: MaterialCardView
    private lateinit var miniTitle: TextView
    private lateinit var miniArtist: TextView
    private lateinit var miniPlayPause: ImageButton
    private lateinit var connectionDot: View
    private lateinit var bottomNavigation: BottomNavigationView
    private lateinit var batteryButton: Button
    private var selectedTab = TAB_STATUS
    private var tabPages: List<ScrollView> = emptyList()
    private var playbackTrackRows: List<PlaybackTrackRow> = emptyList()
    private var pendingDownload: SharedFile? = null
    private val releaseExecutor = Executors.newSingleThreadExecutor()
    private val releaseRefreshGeneration = AtomicLong()
    private val artworkExecutor = Executors.newSingleThreadExecutor()
    private val artworkRefreshGeneration = AtomicLong()
    private var loadedArtworkKey: String? = null
    private var releaseChanges: AutoCloseable? = null
    private var playbackController: MediaController? = null
    private var playbackControllerFuture: ListenableFuture<MediaController>? = null
    private val playbackHandler = Handler(Looper.getMainLooper())
    private val playbackProgress = object : Runnable {
        override fun run() {
            refreshPlaybackControls()
            playbackHandler.postDelayed(this, 1_000)
        }
    }
    private val playbackListener = object : Player.Listener {
        override fun onEvents(player: Player, events: Player.Events) = refreshPlaybackControls()
    }
    private val preferenceListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == KEY_PAGER_MESSAGE) runOnUiThread(::refreshPagerMessage)
        if (key == KEY_FILES_GENERATION) runOnUiThread(::refreshFiles)
        if (key == PlaybackQueueRepository.KEY_QUEUE) runOnUiThread(::refreshPlaybackQueue)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        selectedTab = savedInstanceState?.getInt(STATE_SELECTED_TAB, TAB_STATUS)
            ?: intent.getIntExtra(EXTRA_OPEN_TAB, TAB_STATUS)
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        buildContentView()
        handlePairingIntent(intent)
        handleSharedFileIntent(intent)
        openRequestedTab(intent)
    }

    override fun onSaveInstanceState(outState: Bundle) {
        outState.putInt(STATE_SELECTED_TAB, selectedTab)
        super.onSaveInstanceState(outState)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handlePairingIntent(intent)
        handleSharedFileIntent(intent)
        openRequestedTab(intent)
    }

    override fun onStart() {
        super.onStart()
        ReleaseInstaller.foregroundActivity = this
        preferences.registerOnSharedPreferenceChangeListener(preferenceListener)
        releaseChanges = ReleaseChanges.subscribe(::refreshReleases)
        refreshPagerMessage()
        refreshReleases(reconcile = true)
        refreshFiles()
        refreshPlaybackQueue()
        refreshBatteryOptimizationState()
        connectPlaybackController()
        playbackHandler.post(playbackProgress)
    }

    override fun onStop() {
        releaseChanges?.close()
        releaseChanges = null
        if (ReleaseInstaller.foregroundActivity === this) {
            ReleaseInstaller.foregroundActivity = null
        }
        preferences.unregisterOnSharedPreferenceChangeListener(preferenceListener)
        playbackHandler.removeCallbacks(playbackProgress)
        disconnectPlaybackController()
        super.onStop()
    }

    override fun onDestroy() {
        releaseExecutor.shutdownNow()
        artworkRefreshGeneration.incrementAndGet()
        artworkExecutor.shutdownNow()
        super.onDestroy()
    }

    private fun buildContentView() {
        preferences = getSharedPreferences(PREFERENCES, MODE_PRIVATE)
        endpoint = TextInputEditText(this).apply {
            setText(preferences.getString(KEY_ENDPOINT, ""))
            setTextColor(MigiPalette.text)
            setHintTextColor(MigiPalette.muted)
            inputType = android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        certificatePin = TextInputEditText(this).apply {
            setText(preferences.getString(KEY_CERTIFICATE_PIN, ""))
            setTextColor(MigiPalette.text)
            setHintTextColor(MigiPalette.muted)
            inputType = android.text.InputType.TYPE_CLASS_TEXT
        }
        status = TextView(this).apply {
            val recoveryError = preferences.getString(KEY_CONNECTION_RECOVERY_ERROR, null)
            text = when {
                recoveryError != null -> getString(
                    R.string.connection_recovery_failed,
                    recoveryError,
                )
                ConnectionService.isRunning -> getString(R.string.service_running)
                CredentialStore(this@MainActivity).load() == null -> getString(R.string.device_not_paired)
                else -> getString(R.string.service_stopped)
            }
            applyMigiText(16f, weight = Typeface.BOLD)
        }
        pagerMessage = TextView(this).apply {
            applyMigiText(22f, weight = Typeface.BOLD)
            setLineSpacing(0f, 1.12f)
            setTextIsSelectable(true)
        }

        val start = primaryActionButton(R.string.start_connection).apply {
            setOnClickListener { startConfiguredConnection() }
        }
        val stop = secondaryActionButton(R.string.stop_connection).apply {
            setOnClickListener {
                stopService(Intent(this@MainActivity, ConnectionService::class.java))
                updateStatus(R.string.service_stopped)
            }
        }
        batteryButton = secondaryActionButton(R.string.allow_reliable_background_delivery).apply {
            setOnClickListener {
                requestBatteryOptimizationExemption()
            }
        }
        val dndOverride = SwitchMaterial(this).apply {
            text = getString(R.string.play_sounds_during_dnd)
            applyMigiText(15f)
            thumbTintList = ColorStateList(
                arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                intArrayOf(MigiPalette.primary, MigiPalette.muted),
            )
            trackTintList = ColorStateList(
                arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                intArrayOf(0x806F88E8.toInt(), MigiPalette.outline),
            )
            isChecked = preferences.getBoolean(KEY_DND_OVERRIDE, false)
            setOnCheckedChangeListener { _, enabled ->
                preferences.edit().putBoolean(KEY_DND_OVERRIDE, enabled).apply()
            }
        }
        val audioVolumeLabel = TextView(this).apply {
            applyMigiText(14f, MigiPalette.muted)
        }
        val audioVolume = SeekBar(this).apply {
            max = 100
            progressTintList = ColorStateList.valueOf(MigiPalette.primary)
            thumbTintList = ColorStateList.valueOf(MigiPalette.primary)
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
        val statusPage = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, dp(24), padding, dp(36))
            addView(screenHeader(R.string.home_title, R.string.home_subtitle), matchWidth())
            addGap(30)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(color = MigiPalette.surfaceHigh, radiusDp = 22)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.HORIZONTAL
                    gravity = Gravity.CENTER_VERTICAL
                    setPadding(dp(20), dp(18), dp(20), dp(18))
                    connectionDot = View(this@MainActivity).apply {
                        background = roundedDrawable(
                            if (ConnectionService.isRunning) MigiPalette.secondary else MigiPalette.muted,
                            dp(6).toFloat(),
                        )
                    }
                    addView(connectionDot, LinearLayout.LayoutParams(dp(11), dp(11)).apply {
                        marginEnd = dp(14)
                    })
                    addView(LinearLayout(this@MainActivity).apply {
                        orientation = LinearLayout.VERTICAL
                        addView(sectionLabel(R.string.home_connection_label), matchWidth())
                        addGap(5)
                        addView(status, matchWidth())
                    }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
                    addView(ImageView(this@MainActivity).apply {
                        setImageResource(R.drawable.ic_eye_small)
                        imageTintList = ColorStateList.valueOf(MigiPalette.primary)
                        setPadding(dp(10), dp(10), dp(10), dp(10))
                        background = roundedDrawable(MigiPalette.surfaceBright, dp(22).toFloat())
                    }, LinearLayout.LayoutParams(dp(44), dp(44)).apply { marginStart = dp(12) })
                }, matchWidth())
            }, matchWidth())
            addGap(16)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(color = MigiPalette.surface, radiusDp = 26)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL
                    setPadding(dp(22), dp(22), dp(22), dp(24))
                    addView(sectionLabel(R.string.home_pager_label), matchWidth())
                    addGap(14)
                    addView(pagerMessage, matchWidth())
                }, matchWidth())
            }, matchWidth())
        }
        val playbackPage = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, dp(24), padding, dp(36))
            addView(screenHeader(R.string.tab_playback, R.string.music_subtitle), matchWidth())
            addGap(24)
            val artworkSize = minOf(resources.displayMetrics.widthPixels - dp(40), dp(360))
            playbackArtwork = PlaylistArtworkView(this@MainActivity).apply {
                showFallback("migi")
            }
            addView(playbackArtwork, LinearLayout.LayoutParams(artworkSize, artworkSize).apply {
                gravity = Gravity.CENTER_HORIZONTAL
            })
            addGap(24)
            playbackHeroLabel = sectionLabel(R.string.playlist_ready_label)
            addView(playbackHeroLabel, matchWidth())
            addGap(8)
            playbackCurrent = TextView(this@MainActivity).apply {
                setText(R.string.playback_nothing_playing)
                applyMigiText(27f, weight = Typeface.BOLD)
                maxLines = 2
            }
            addView(playbackCurrent, matchWidth())
            addGap(6)
            playbackArtist = TextView(this@MainActivity).apply {
                setText(R.string.playback_unknown_artist)
                applyMigiText(16f, MigiPalette.muted)
                maxLines = 1
            }
            addView(playbackArtist, matchWidth())
            addGap(20)
            playbackSeek = Slider(this@MainActivity).apply {
                valueFrom = 0f
                valueTo = PLAYBACK_SEEK_MAX.toFloat()
                value = 0f
                isEnabled = false
                trackActiveTintList = ColorStateList.valueOf(MigiPalette.primary)
                trackInactiveTintList = ColorStateList.valueOf(MigiPalette.outline)
                thumbTintList = ColorStateList.valueOf(MigiPalette.primary)
                haloTintList = ColorStateList.valueOf(0x30829BFF)
                trackHeight = dp(4)
                thumbRadius = dp(7)
                addOnSliderTouchListener(object : Slider.OnSliderTouchListener {
                    override fun onStartTrackingTouch(slider: Slider) = Unit
                    override fun onStopTrackingTouch(slider: Slider) {
                        val controller = playbackController ?: return
                        val duration = controller.duration.takeIf { it > 0 && it != C.TIME_UNSET } ?: return
                        controller.seekTo(duration * slider.value.toLong() / PLAYBACK_SEEK_MAX)
                    }
                })
            }
            addView(playbackSeek, matchWidth())
            playbackPosition = TextView(this@MainActivity).apply {
                text = getString(R.string.playback_position, "0:00", "0:00")
                applyMigiText(12f, MigiPalette.muted)
                gravity = Gravity.END
            }
            addView(playbackPosition, matchWidth())
            addGap(12)
            val transportControls = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER
                playbackPrevious = iconButton(
                    R.drawable.ic_previous,
                    getString(R.string.content_previous),
                    sizeDp = 54,
                ).apply { setOnClickListener { playbackController?.seekToPreviousMediaItem() } }
                playbackPlayPause = iconButton(
                    R.drawable.ic_play,
                    getString(R.string.content_play_pause),
                    backgroundColor = MigiPalette.primary,
                    tint = MigiPalette.onPrimary,
                    sizeDp = 68,
                ).apply {
                    setOnClickListener {
                        playbackController?.let { if (it.isPlaying) it.pause() else it.play() }
                    }
                }
                playbackNext = iconButton(
                    R.drawable.ic_next,
                    getString(R.string.content_next),
                    sizeDp = 54,
                ).apply { setOnClickListener { playbackController?.seekToNextMediaItem() } }
                addView(playbackPrevious, LinearLayout.LayoutParams(dp(54), dp(54)))
                addView(playbackPlayPause, LinearLayout.LayoutParams(dp(68), dp(68)).apply {
                    marginStart = dp(22)
                    marginEnd = dp(22)
                })
                addView(playbackNext, LinearLayout.LayoutParams(dp(54), dp(54)))
            }
            addView(transportControls, matchWidth())
            addGap(14)
            val playbackModes = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER
                playbackShuffle = iconButton(
                    R.drawable.ic_shuffle,
                    getString(R.string.content_shuffle),
                    sizeDp = 44,
                ).apply {
                    setOnClickListener {
                        playbackController?.let { it.shuffleModeEnabled = !it.shuffleModeEnabled }
                    }
                }
                playbackRepeat = iconButton(
                    R.drawable.ic_repeat,
                    getString(R.string.content_repeat),
                    sizeDp = 44,
                ).apply {
                    setOnClickListener {
                        playbackController?.let {
                            it.repeatMode = when (it.repeatMode) {
                                Player.REPEAT_MODE_OFF -> Player.REPEAT_MODE_ALL
                                Player.REPEAT_MODE_ALL -> Player.REPEAT_MODE_ONE
                                else -> Player.REPEAT_MODE_OFF
                            }
                        }
                    }
                }
                addView(playbackShuffle, LinearLayout.LayoutParams(dp(44), dp(44)).apply { marginEnd = dp(16) })
                addView(playbackRepeat, LinearLayout.LayoutParams(dp(44), dp(44)))
            }
            addView(playbackModes, matchWidth())
            playbackStatus = TextView(this@MainActivity).apply {
                applyMigiText(13f, MigiPalette.muted)
                gravity = Gravity.CENTER
                setPadding(0, dp(12), 0, 0)
            }
            addView(playbackStatus, matchWidth())
            addGap(18)
            playbackButton = primaryActionButton(R.string.playback_start_playlist).apply {
                visibility = View.GONE
                setOnClickListener { prepareAndPlayQueue() }
            }
            addView(playbackButton, matchWidth())
            playbackStopButton = secondaryActionButton(R.string.playback_end_session, R.drawable.ic_stop).apply {
                visibility = View.GONE
                setOnClickListener {
                    playbackController?.stop()
                    playbackController?.clearMediaItems()
                    PlaybackService.stop(this@MainActivity)
                    playbackStatus.setText(R.string.playback_stopped)
                }
            }
            addView(playbackStopButton, matchWidth().apply { topMargin = dp(10) })
            addGap(34)
            addView(sectionLabel(R.string.queue_title), matchWidth())
            playbackSummary = TextView(this@MainActivity).apply {
                applyMigiText(14f, MigiPalette.muted)
                setPadding(0, dp(7), 0, dp(14))
            }
            addView(playbackSummary, matchWidth())
            playbackTracks = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
            }
            addView(playbackTracks, matchWidth())
        }
        val filesPage = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, dp(24), padding, dp(36))
            addView(screenHeader(R.string.tab_files, R.string.files_subtitle), matchWidth())
            addGap(24)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(color = MigiPalette.surfaceHigh, radiusDp = 24)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL
                    setPadding(dp(20), dp(20), dp(20), dp(20))
                    addView(TextView(this@MainActivity).apply {
                        setText(R.string.files_send_title)
                        applyMigiText(20f, weight = Typeface.BOLD)
                    }, matchWidth())
                    addGap(7)
                    addView(TextView(this@MainActivity).apply {
                        setText(R.string.files_send_body)
                        applyMigiText(14f, MigiPalette.muted)
                        setLineSpacing(0f, 1.15f)
                    }, matchWidth())
                    addGap(18)
                    addView(LinearLayout(this@MainActivity).apply {
                        orientation = LinearLayout.HORIZONTAL
                        addView(primaryActionButton(R.string.choose_file, R.drawable.ic_upload).apply {
                            setOnClickListener {
                                startActivityForResult(
                                    Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
                                        addCategory(Intent.CATEGORY_OPENABLE)
                                        type = "*/*"
                                    },
                                    REQUEST_CHOOSE_FILE,
                                )
                            }
                        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                            marginEnd = dp(6)
                        })
                        addView(secondaryActionButton(R.string.choose_photo, R.drawable.ic_photo).apply {
                            setOnClickListener {
                                startActivityForResult(
                                    Intent(MediaStore.ACTION_PICK_IMAGES).apply { type = "image/*" },
                                    REQUEST_CHOOSE_PHOTO,
                                )
                            }
                        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                            marginStart = dp(6)
                        })
                    }, matchWidth())
                }, matchWidth())
            }, matchWidth())
            addGap(26)
            fileList = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
            }
            addView(fileList, matchWidth())
        }
        val updatesPage = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, dp(24), padding, dp(36))
            addView(screenHeader(R.string.tab_updates, R.string.updates_subtitle), matchWidth())
            addGap(24)
            releaseList = LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
            }
            addView(releaseList, matchWidth())
        }
        val advancedConnectionSettings = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            visibility = View.GONE
            setPadding(0, dp(12), 0, 0)
            addView(inputContainer(R.string.endpoint_hint, endpoint), matchWidth())
            addGap(10)
            addView(inputContainer(R.string.certificate_pin_hint, certificatePin), matchWidth())
        }
        val settingsPage = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, dp(24), padding, dp(36))
            addView(screenHeader(R.string.tab_settings, R.string.settings_subtitle), matchWidth())
            addGap(24)
            addView(sectionLabel(R.string.settings_connection), matchWidth())
            addGap(10)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(radiusDp = 22)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL
                    setPadding(dp(18), dp(18), dp(18), dp(18))
                    settingsStatus = TextView(this@MainActivity).apply {
                        text = status.text
                        applyMigiText(16f, weight = Typeface.BOLD)
                    }
                    addView(settingsStatus, matchWidth())
                    addGap(14)
                    addView(secondaryActionButton(R.string.show_advanced_connection_settings).apply {
                        setOnClickListener {
                            val show = advancedConnectionSettings.visibility != View.VISIBLE
                            advancedConnectionSettings.visibility = if (show) View.VISIBLE else View.GONE
                            setText(
                                if (show) R.string.hide_advanced_connection_settings
                                else R.string.show_advanced_connection_settings,
                            )
                        }
                    }, matchWidth())
                    addView(advancedConnectionSettings, matchWidth())
                    addGap(12)
                    addView(start, matchWidth())
                    addView(stop, matchWidth().apply { topMargin = dp(8) })
                    addView(batteryButton, matchWidth().apply { topMargin = dp(8) })
                }, matchWidth())
            }, matchWidth())
            addGap(26)
            addView(sectionLabel(R.string.settings_sound), matchWidth())
            addGap(10)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(radiusDp = 22)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL
                    setPadding(dp(18), dp(16), dp(18), dp(16))
                    addView(dndOverride, matchWidth())
                    addGap(14)
                    addView(audioVolumeLabel, matchWidth())
                    addView(audioVolume, matchWidth())
                }, matchWidth())
            }, matchWidth())
            addGap(26)
            addView(sectionLabel(R.string.settings_about), matchWidth())
            addGap(10)
            addView(MaterialCardView(this@MainActivity).apply {
                applyMigiCard(radiusDp = 22)
                addView(TextView(this@MainActivity).apply {
                    text = getString(R.string.app_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE)
                    applyMigiText(15f, MigiPalette.muted)
                    setPadding(dp(18), dp(18), dp(18), dp(18))
                }, matchWidth())
            }, matchWidth())
        }

        tabPages = listOf(statusPage, playbackPage, filesPage, updatesPage, settingsPage).map { page ->
            ScrollView(this).apply {
                isFillViewport = true
                isVerticalScrollBarEnabled = false
                clipToPadding = false
                addView(page, matchWidth())
            }
        }
        val pageHost = FrameLayout(this).apply {
            for (page in tabPages) {
                addView(
                    page,
                    FrameLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT,
                        ViewGroup.LayoutParams.MATCH_PARENT,
                    ),
                )
            }
        }
        miniPlayer = buildMiniPlayer()
        bottomNavigation = BottomNavigationView(this).apply {
            menu.add(0, NAV_HOME, 0, R.string.tab_status).setIcon(R.drawable.ic_nav_home)
            menu.add(0, NAV_MUSIC, 1, R.string.tab_playback).setIcon(R.drawable.ic_nav_music)
            menu.add(0, NAV_FILES, 2, R.string.tab_files).setIcon(R.drawable.ic_nav_files)
            menu.add(0, NAV_UPDATES, 3, R.string.tab_updates).setIcon(R.drawable.ic_nav_updates)
            menu.add(0, NAV_SETTINGS, 4, R.string.tab_settings).setIcon(R.drawable.ic_nav_settings)
            val navigationColors = ColorStateList(
                arrayOf(intArrayOf(android.R.attr.state_checked), intArrayOf()),
                intArrayOf(MigiPalette.primary, MigiPalette.muted),
            )
            itemIconTintList = navigationColors
            itemTextColor = navigationColors
            itemActiveIndicatorColor = ColorStateList.valueOf(0x332F4F91)
            isItemActiveIndicatorEnabled = true
            labelVisibilityMode = BottomNavigationView.LABEL_VISIBILITY_LABELED
            setBackgroundColor(MigiPalette.surface)
            elevation = 0f
            setPadding(dp(4), dp(5), dp(4), dp(4))
            setOnItemSelectedListener { item ->
                val index = NAVIGATION_IDS.indexOf(item.itemId)
                if (index >= 0) showTab(index)
                index >= 0
            }
        }
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(MigiPalette.background)
            addView(
                pageHost,
                LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f),
            )
            addView(miniPlayer, matchWidth().apply {
                leftMargin = dp(12)
                rightMargin = dp(12)
                bottomMargin = dp(8)
            })
            addView(bottomNavigation, matchWidth())
        }
        ViewCompat.setOnApplyWindowInsetsListener(root) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.setPadding(0, bars.top, 0, bars.bottom)
            insets
        }
        setContentView(root)
        showTab(selectedTab)
    }

    private fun showTab(index: Int) {
        selectedTab = index.coerceIn(tabPages.indices)
        tabPages.forEachIndexed { tabIndex, page ->
            page.visibility = if (tabIndex == selectedTab) View.VISIBLE else View.GONE
        }
        if (::bottomNavigation.isInitialized) {
            val navigationID = NAVIGATION_IDS[selectedTab]
            if (bottomNavigation.selectedItemId != navigationID) {
                bottomNavigation.selectedItemId = navigationID
            }
        }
    }

    private fun openRequestedTab(intent: Intent?) {
        if (tabPages.isEmpty()) return
        if (intent?.hasExtra(EXTRA_OPEN_TAB) == true) {
            showTab(intent.getIntExtra(EXTRA_OPEN_TAB, TAB_STATUS))
            intent.removeExtra(EXTRA_OPEN_TAB)
        }
    }

    private fun screenHeader(title: Int, subtitle: Int): LinearLayout = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        addView(TextView(this@MainActivity).apply {
            setText(title)
            applyMigiText(34f, weight = Typeface.BOLD)
            letterSpacing = -0.035f
        }, matchWidth())
        addGap(7)
        addView(TextView(this@MainActivity).apply {
            setText(subtitle)
            applyMigiText(15f, MigiPalette.muted)
            setLineSpacing(0f, 1.12f)
        }, matchWidth())
    }

    private fun sectionLabel(text: Int): TextView = TextView(this).apply {
        setText(text)
        applyMigiText(11f, MigiPalette.primary, Typeface.BOLD)
        letterSpacing = 0.12f
    }

    private fun primaryActionButton(text: Int, icon: Int = 0): MaterialButton =
        materialActionButton(text, icon, primary = true)

    private fun secondaryActionButton(text: Int, icon: Int = 0): MaterialButton =
        materialActionButton(text, icon, primary = false)

    private fun materialActionButton(text: Int, icon: Int, primary: Boolean): MaterialButton =
        MaterialButton(this).apply {
            setText(text)
            isAllCaps = false
            textSize = 14f
            typeface = Typeface.create("sans-serif", Typeface.BOLD)
            minimumHeight = dp(52)
            minHeight = dp(52)
            insetTop = 0
            insetBottom = 0
            cornerRadius = dp(16)
            backgroundTintList = ColorStateList.valueOf(
                if (primary) MigiPalette.primary else MigiPalette.surfaceHigh,
            )
            setTextColor(if (primary) MigiPalette.onPrimary else MigiPalette.text)
            strokeWidth = if (primary) 0 else dp(1)
            strokeColor = ColorStateList.valueOf(MigiPalette.outline)
            if (icon != 0) {
                setIconResource(icon)
                iconTint = ColorStateList.valueOf(if (primary) MigiPalette.onPrimary else MigiPalette.primary)
                iconPadding = dp(8)
                iconGravity = MaterialButton.ICON_GRAVITY_TEXT_START
            }
        }

    private fun iconButton(
        icon: Int,
        description: String,
        backgroundColor: Int = MigiPalette.surfaceHigh,
        tint: Int = MigiPalette.text,
        sizeDp: Int = 48,
    ): ImageButton = ImageButton(this).apply {
        setImageResource(icon)
        imageTintList = ColorStateList.valueOf(tint)
        background = rippleDrawable(this@MainActivity, backgroundColor, sizeDp / 2)
        contentDescription = description
        scaleType = ImageView.ScaleType.CENTER
        setPadding(dp(12), dp(12), dp(12), dp(12))
        minimumWidth = 0
        minimumHeight = 0
    }

    private fun setIconButtonActive(button: ImageButton, active: Boolean) {
        button.background = rippleDrawable(
            this,
            if (active) 0x334F70E2 else MigiPalette.surfaceHigh,
            22,
        )
        button.imageTintList = ColorStateList.valueOf(
            if (active) MigiPalette.primary else MigiPalette.muted,
        )
        button.alpha = if (button.isEnabled) 1f else 0.38f
    }

    private fun inputContainer(hint: Int, input: EditText): TextInputLayout = TextInputLayout(this).apply {
        this.hint = getString(hint)
        boxBackgroundMode = TextInputLayout.BOX_BACKGROUND_OUTLINE
        boxBackgroundColor = MigiPalette.surfaceHigh
        boxStrokeColor = MigiPalette.primary
        defaultHintTextColor = ColorStateList.valueOf(MigiPalette.muted)
        setBoxCornerRadii(
            dp(15).toFloat(),
            dp(15).toFloat(),
            dp(15).toFloat(),
            dp(15).toFloat(),
        )
        addView(input, matchWidth())
    }

    private fun emptyStateCard(title: String, body: String? = null): MaterialCardView =
        MaterialCardView(this).apply {
            applyMigiCard(color = MigiPalette.surface, radiusDp = 20)
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(dp(20), dp(20), dp(20), dp(20))
                addView(TextView(this@MainActivity).apply {
                    text = title
                    applyMigiText(16f, weight = Typeface.BOLD)
                }, matchWidth())
                body?.takeIf { it.isNotBlank() }?.let { message ->
                    addGap(7)
                    addView(TextView(this@MainActivity).apply {
                        text = message
                        applyMigiText(14f, MigiPalette.muted)
                        setLineSpacing(0f, 1.15f)
                    }, matchWidth())
                }
            }, matchWidth())
        }

    private fun buildMiniPlayer(): MaterialCardView = MaterialCardView(this).apply {
        applyMigiCard(color = MigiPalette.surfaceHigh, radiusDp = 20)
        visibility = View.GONE
        isClickable = true
        isFocusable = true
        foreground = rippleDrawable(this@MainActivity, Color.TRANSPARENT, 20)
        contentDescription = getString(R.string.mini_player_description)
        setOnClickListener { showTab(TAB_MUSIC) }
        addView(LinearLayout(this@MainActivity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(8), dp(8), dp(8), dp(8))
            miniArtwork = PlaylistArtworkView(this@MainActivity).apply {
                cornerRadius = dp(13).toFloat()
                showFallback("migi")
            }
            addView(miniArtwork, LinearLayout.LayoutParams(dp(52), dp(52)))
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                miniTitle = TextView(this@MainActivity).apply {
                    applyMigiText(14f, weight = Typeface.BOLD)
                    maxLines = 1
                }
                addView(miniTitle, matchWidth())
                addGap(4)
                miniArtist = TextView(this@MainActivity).apply {
                    applyMigiText(12f, MigiPalette.muted)
                    maxLines = 1
                }
                addView(miniArtist, matchWidth())
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                marginStart = dp(13)
                marginEnd = dp(8)
            })
            miniPlayPause = iconButton(
                R.drawable.ic_play,
                getString(R.string.content_play_pause),
                backgroundColor = MigiPalette.primary,
                tint = MigiPalette.onPrimary,
                sizeDp = 46,
            ).apply {
                setOnClickListener {
                    playbackController?.let { if (it.isPlaying) it.pause() else it.play() }
                }
            }
            addView(miniPlayPause, LinearLayout.LayoutParams(dp(46), dp(46)))
        }, matchWidth())
    }

    private fun loadPlaylistArtwork(queue: PlaybackQueue) {
        val artwork = queue.artwork ?: run {
            loadedArtworkKey = null
            artworkRefreshGeneration.incrementAndGet()
            return
        }
        val key = "${artwork.id}:${artwork.sha256}"
        if (loadedArtworkKey == key) return
        loadedArtworkKey = key
        val generation = artworkRefreshGeneration.incrementAndGet()
        artworkExecutor.execute {
            val result = runCatching {
                decodeArtwork(PlaybackMediaCache(applicationContext).prepare(artwork))
                    ?: error("Artwork cannot be decoded")
            }
            runOnUiThread {
                if (isDestroyed || generation != artworkRefreshGeneration.get()) return@runOnUiThread
                result.onSuccess { bitmap ->
                    playbackArtwork.showArtwork(queue.name, bitmap)
                    miniArtwork.showArtwork(queue.name, bitmap)
                }.onFailure {
                    loadedArtworkKey = null
                    playbackArtwork.showFallback(queue.name)
                    miniArtwork.showFallback(queue.name)
                }
            }
        }
    }

    private fun refreshPlaybackTrackSelection(activeIndex: Int) {
        for ((index, row) in playbackTrackRows.withIndex()) {
            val active = index == activeIndex
            row.card.setCardBackgroundColor(if (active) 0xFF1E2B48.toInt() else MigiPalette.surface)
            row.card.strokeColor = if (active) MigiPalette.primary else MigiPalette.outline
            row.number.setTextColor(if (active) MigiPalette.primary else MigiPalette.muted)
            row.title.setTextColor(MigiPalette.text)
            row.subtitle.setTextColor(if (active) 0xFFC4CEEC.toInt() else MigiPalette.muted)
        }
    }

    private fun LinearLayout.addGap(heightDp: Int) {
        addView(View(this@MainActivity), LinearLayout.LayoutParams(1, dp(heightDp)))
    }

    private fun connectPlaybackController() {
        if (playbackControllerFuture != null) return
        val token = SessionToken(this, ComponentName(this, PlaybackService::class.java))
        val future = MediaController.Builder(this, token).buildAsync()
        playbackControllerFuture = future
        future.addListener({
            if (playbackControllerFuture !== future) {
                return@addListener
            }
            runCatching { future.get() }
                .onSuccess { controller ->
                    playbackController = controller
                    controller.addListener(playbackListener)
                    refreshPlaybackControls()
                }
                .onFailure {
                    playbackControllerFuture = null
                    MediaController.releaseFuture(future)
                    refreshPlaybackControls()
                }
        }, mainExecutor)
    }

    private fun disconnectPlaybackController() {
        playbackController?.removeListener(playbackListener)
        playbackController = null
        playbackControllerFuture?.let(MediaController::releaseFuture)
        playbackControllerFuture = null
    }

    private fun refreshPlaybackControls() {
        if (!::playbackPlayPause.isInitialized) return
        val controller = playbackController
        val hasMedia = controller != null && controller.mediaItemCount > 0
        val isPlaying = controller?.isPlaying == true
        val queue = PlaybackQueueRepository(this).current()
        playbackPlayPause.isEnabled = hasMedia
        playbackPrevious.isEnabled = controller?.hasPreviousMediaItem() == true
        playbackNext.isEnabled = controller?.hasNextMediaItem() == true
        playbackShuffle.isEnabled = hasMedia
        playbackRepeat.isEnabled = hasMedia
        playbackPrevious.alpha = if (playbackPrevious.isEnabled) 1f else 0.32f
        playbackNext.alpha = if (playbackNext.isEnabled) 1f else 0.32f
        playbackPlayPause.alpha = if (hasMedia) 1f else 0.38f
        playbackPlayPause.setImageResource(if (isPlaying) R.drawable.ic_pause else R.drawable.ic_play)
        miniPlayPause.setImageResource(if (isPlaying) R.drawable.ic_pause else R.drawable.ic_play)
        miniPlayPause.isEnabled = hasMedia
        miniPlayPause.alpha = if (hasMedia) 1f else 0.38f
        setIconButtonActive(playbackShuffle, controller?.shuffleModeEnabled == true)
        setIconButtonActive(playbackRepeat, hasMedia && controller?.repeatMode != Player.REPEAT_MODE_OFF)
        playbackShuffle.contentDescription = getString(
            if (controller?.shuffleModeEnabled == true) R.string.playback_shuffle_on else R.string.playback_shuffle_off,
        )
        playbackRepeat.contentDescription = getString(when (controller?.repeatMode) {
            Player.REPEAT_MODE_ALL -> R.string.playback_repeat_all
            Player.REPEAT_MODE_ONE -> R.string.playback_repeat_one
            else -> R.string.playback_repeat_off
        })
        val metadata = controller?.currentMediaItem?.mediaMetadata
        val title = metadata?.title?.toString()?.takeIf { it.isNotBlank() }
            ?: queue?.name
            ?: getString(R.string.playback_nothing_playing)
        val artist = metadata?.artist?.toString()?.takeIf { it.isNotBlank() }
            ?: queue?.takeIf { !hasMedia }?.let {
                resources.getQuantityString(R.plurals.queue_tracks, it.items.size, it.items.size, it.agent)
            }
            ?: getString(R.string.playback_unknown_artist)
        playbackCurrent.text = title
        playbackArtist.text = artist
        playbackHeroLabel.setText(if (hasMedia) R.string.now_playing_label else R.string.playlist_ready_label)
        miniTitle.text = title
        miniArtist.text = artist
        miniPlayer.visibility = if (hasMedia) View.VISIBLE else View.GONE
        playbackButton.visibility = if (queue != null && !hasMedia) View.VISIBLE else View.GONE
        playbackStopButton.visibility = if (hasMedia) View.VISIBLE else View.GONE
        val duration = controller?.duration?.takeIf { it > 0 && it != C.TIME_UNSET } ?: 0L
        val position = controller?.currentPosition?.coerceAtLeast(0L)?.coerceAtMost(duration) ?: 0L
        playbackPosition.text = getString(
            R.string.playback_position,
            formatPlaybackTime(position),
            formatPlaybackTime(duration),
        )
        playbackSeek.isEnabled = duration > 0
        if (!playbackSeek.isPressed) {
            playbackSeek.value = if (duration > 0) {
                (position * PLAYBACK_SEEK_MAX / duration).toFloat()
            } else 0f
        }
        refreshPlaybackTrackSelection(controller?.currentMediaItemIndex ?: -1)
    }

    private fun formatPlaybackTime(milliseconds: Long): String {
        val totalSeconds = milliseconds.coerceAtLeast(0L) / 1_000
        val hours = totalSeconds / 3_600
        val minutes = totalSeconds % 3_600 / 60
        val seconds = totalSeconds % 60
        return if (hours > 0) "%d:%02d:%02d".format(hours, minutes, seconds)
        else "%d:%02d".format(minutes, seconds)
    }

    private fun refreshPlaybackQueue() {
        if (!::playbackSummary.isInitialized) return
        val queue = PlaybackQueueRepository(this).current()
        playbackTracks.removeAllViews()
        playbackTrackRows = emptyList()
        if (queue == null) {
            playbackSummary.setText(R.string.playback_empty)
            playbackStatus.text = ""
            playbackButton.isEnabled = false
            playbackArtwork.showFallback("migi")
            miniArtwork.showFallback("migi")
            loadedArtworkKey = null
            artworkRefreshGeneration.incrementAndGet()
            refreshPlaybackControls()
            return
        }
        playbackSummary.text = resources.getQuantityString(
            R.plurals.queue_tracks,
            queue.items.size,
            queue.items.size,
            queue.agent,
        )
        playbackStatus.setText(R.string.playback_ready_to_prepare)
        playbackButton.isEnabled = true
        playbackArtwork.showFallback(queue.name)
        miniArtwork.showFallback(queue.name)
        loadPlaylistArtwork(queue)
        val rows = ArrayList<PlaybackTrackRow>(queue.items.size)
        for ((index, track) in queue.items.withIndex()) {
            val number = TextView(this).apply {
                text = getString(R.string.playback_track_number, index + 1)
                applyMigiText(12f, MigiPalette.muted, Typeface.BOLD)
                gravity = Gravity.CENTER
            }
            val title = TextView(this).apply {
                text = track.title
                applyMigiText(15f, weight = Typeface.BOLD)
                maxLines = 1
            }
            val subtitle = TextView(this).apply {
                text = buildString {
                    append(track.artist.ifBlank { getString(R.string.playback_unknown_artist) })
                    append(" · ")
                    append(formatFileSize(track.size))
                }
                applyMigiText(12f, MigiPalette.muted)
                maxLines = 1
            }
            val card = MaterialCardView(this).apply {
                applyMigiCard(radiusDp = 18)
                isClickable = true
                isFocusable = true
                foreground = rippleDrawable(this@MainActivity, Color.TRANSPARENT, 18)
                addView(LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.HORIZONTAL
                    gravity = Gravity.CENTER_VERTICAL
                    setPadding(dp(12), dp(13), dp(14), dp(13))
                    addView(number, LinearLayout.LayoutParams(dp(38), dp(38)))
                    addView(LinearLayout(this@MainActivity).apply {
                        orientation = LinearLayout.VERTICAL
                        addView(title, matchWidth())
                        addGap(4)
                        addView(subtitle, matchWidth())
                    }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                        marginStart = dp(10)
                    })
                }, matchWidth())
                setOnClickListener {
                    playbackController?.takeIf { it.mediaItemCount > index }?.let { controller ->
                        controller.seekTo(index, 0)
                        controller.play()
                    }
                }
            }
            playbackTracks.addView(card, matchWidth().apply { bottomMargin = dp(9) })
            rows += PlaybackTrackRow(card, number, title, subtitle)
        }
        playbackTrackRows = rows
        refreshPlaybackControls()
    }

    private fun prepareAndPlayQueue() {
        val queue = PlaybackQueueRepository(this).current() ?: run {
            refreshPlaybackQueue()
            return
        }
        playbackButton.isEnabled = false
        playbackStatus.setText(R.string.playback_starting)
        PlaybackService.start(this, queue.eventID) { playbackError ->
            if (!isDestroyed) {
                if (playbackError == null) {
                    playbackStatus.setText(R.string.playback_started)
                } else {
                    playbackStatus.text = getString(
                        R.string.playback_failed,
                        playbackError.message ?: "unknown error",
                    )
                }
                playbackButton.isEnabled = true
            }
        }
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        when (requestCode) {
            REQUEST_CHOOSE_FILE,
            REQUEST_CHOOSE_PHOTO -> handleUploadPickerResult(resultCode, data)
            REQUEST_SAVE_FILE -> {
                val file = pendingDownload
                    .also { pendingDownload = null }
                    ?: return
                if (resultCode != RESULT_OK) {
                    updateStatus(R.string.file_selection_cancelled)
                    return
                }
                val destination = data?.data ?: run {
                    updateStatus(R.string.file_share_invalid)
                    return
                }
                updateStatus(R.string.file_downloading)
                thread(name = "migi-file-download") {
                    val result = runCatching { FileExchangeClient(this).download(file, destination) }
                    runOnUiThread {
                        updateStatus(result.fold(
                            onSuccess = { getString(R.string.file_downloaded, file.name) },
                            onFailure = { getString(R.string.file_transfer_failed, it.message ?: "unknown error") },
                        ))
                    }
                }
            }
        }
    }

    private fun handleUploadPickerResult(resultCode: Int, data: Intent?) {
        val uri = data?.data
        when (PickerResultPolicy.classify(resultCode == RESULT_OK, uri != null)) {
            PickerResultPolicy.Outcome.CANCELLED -> {
                updateStatus(R.string.file_selection_cancelled)
                return
            }
            PickerResultPolicy.Outcome.MISSING_URI -> {
                updateStatus(R.string.file_share_invalid)
                return
            }
            PickerResultPolicy.Outcome.SELECTED -> uploadSharedFile(requireNotNull(uri))
        }
    }

    private fun refreshPagerMessage() {
        pagerMessage.text = preferences.getString(KEY_PAGER_MESSAGE, null)
            ?.takeIf { it.isNotBlank() }
            ?: getString(R.string.pager_empty)
    }

    private fun refreshFiles() {
        fileList.removeAllViews()
        fileList.addView(TextView(this).apply {
            setText(R.string.files_loading)
            applyMigiText(14f, MigiPalette.muted)
        })
        thread(name = "migi-file-list") {
            val result = runCatching { FileExchangeClient(this).list() }
            runOnUiThread {
                fileList.removeAllViews()
                result.onFailure {
                    fileList.addView(emptyStateCard(getString(R.string.files_failed, it.message ?: "unknown error")))
                }.onSuccess { files ->
                    if (files.isEmpty()) {
                        fileList.addView(emptyStateCard(getString(R.string.files_empty)))
                    }
                    for (file in files) {
                        val content = LinearLayout(this).apply {
                            orientation = LinearLayout.VERTICAL
                            setPadding(dp(18), dp(17), dp(18), dp(18))
                        }
                        content.addView(TextView(this).apply {
                            text = getString(
                                R.string.file_summary,
                                file.name,
                                formatFileSize(file.size),
                                file.source,
                                file.expiresAt.toString(),
                            )
                            applyMigiText(15f, weight = Typeface.BOLD)
                            setLineSpacing(0f, 1.15f)
                            setTextIsSelectable(true)
                        })
                        content.addGap(14)
                        val actions = LinearLayout(this).apply {
                            orientation = LinearLayout.HORIZONTAL
                        }
                        if (file.isViewableHTML()) {
                            actions.addView(secondaryActionButton(R.string.open_html_file).apply {
                                setOnClickListener {
                                    val button = this
                                    button.isEnabled = false
                                    button.setText(R.string.opening_html_file)
                                    updateStatus(R.string.opening_html_file)
                                    thread(name = "migi-html-download") {
                                        val result = runCatching {
                                            FileExchangeClient(this@MainActivity)
                                                .downloadForViewing(file)
                                        }
                                        runOnUiThread {
                                            button.isEnabled = true
                                            button.setText(R.string.open_html_file)
                                            result.onSuccess { temporary ->
                                                updateStatus(
                                                    getString(R.string.html_file_opened, file.name),
                                                )
                                                startActivity(
                                                    HtmlViewerActivity.intent(
                                                        this@MainActivity,
                                                        file.name,
                                                        temporary,
                                                    ),
                                                )
                                            }.onFailure {
                                                val message = getString(
                                                    R.string.file_transfer_failed,
                                                    it.message ?: "unknown error",
                                                )
                                                updateStatus(message)
                                                Toast.makeText(
                                                    this@MainActivity,
                                                    message,
                                                    Toast.LENGTH_LONG,
                                                ).show()
                                            }
                                        }
                                    }
                                }
                            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                                marginEnd = dp(5)
                            })
                        }
                        actions.addView(primaryActionButton(R.string.save_file).apply {
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
                        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f).apply {
                            if (file.isViewableHTML()) marginStart = dp(5)
                        })
                        content.addView(actions, matchWidth())
                        val item = MaterialCardView(this).apply {
                            applyMigiCard(radiusDp = 20)
                            addView(content, matchWidth())
                        }
                        fileList.addView(item, matchWidth().apply { bottomMargin = dp(10) })
                    }
                }
            }
        }
    }

    private fun handleSharedFileIntent(intent: Intent?) {
        if (intent?.action != Intent.ACTION_SEND) return
        val uri = intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java) ?: run {
            updateStatus(R.string.file_share_invalid)
            return
        }
        setIntent(Intent(this, MainActivity::class.java))
        uploadSharedFile(uri)
    }

    private fun uploadSharedFile(uri: Uri) {
        updateStatus(R.string.file_uploading)
        thread(name = "migi-file-upload") {
            val result = runCatching { FileExchangeClient(this).upload(uri) }
            runOnUiThread {
                updateStatus(result.fold(
                    onSuccess = { getString(R.string.file_uploaded, it.name) },
                    onFailure = { getString(R.string.file_transfer_failed, it.message ?: "unknown error") },
                ))
                if (result.isSuccess) refreshFiles()
            }
        }
    }

    private fun formatFileSize(bytes: Long): String = when {
        bytes >= 1024 * 1024 -> "%.1f MiB".format(bytes.toDouble() / (1024 * 1024))
        bytes >= 1024 -> "%.1f KiB".format(bytes.toDouble() / 1024)
        else -> "$bytes B"
    }

    private fun refreshReleases(reconcile: Boolean = false) {
        if (releaseExecutor.isShutdown) return
        val generation = releaseRefreshGeneration.incrementAndGet()
        runCatching {
            releaseExecutor.execute {
                val result = runCatching {
                    if (reconcile) ReleaseInstaller(applicationContext).reconcileSessions()
                    ReleaseRepository(applicationContext).use { it.listReleases() }
                }
                runOnUiThread {
                    if (isDestroyed || generation != releaseRefreshGeneration.get()) {
                        return@runOnUiThread
                    }
                    result.onSuccess(::renderReleases).onFailure {
                        updateStatus(getString(
                            R.string.release_failed,
                            it.message ?: it.javaClass.simpleName,
                        ))
                    }
                }
            }
        }
    }

    private fun renderReleases(releases: List<PendingRelease>) {
        releaseList.removeAllViews()
        if (releases.isEmpty()) {
            releaseList.addView(emptyStateCard(
                getString(R.string.releases_empty),
                getString(R.string.updates_empty_body),
            ))
            return
        }
        for (release in releases) {
            val content = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(dp(18), dp(18), dp(18), dp(18))
            }
            content.addView(TextView(this).apply {
                text = buildString {
                    append(release.artifact.packageName)
                    append(" ")
                    append(release.artifact.versionName)
                    append(" (")
                    append(release.artifact.versionCode)
                    append(") — ")
                    append(release.state)
                    if (release.artifact.packageName == BuildConfig.APPLICATION_ID) {
                        append("\n")
                        append(getString(R.string.self_update_warning))
                    }
                    release.publisher?.takeIf { it.isNotBlank() }?.let {
                        append("\n")
                        append(getString(R.string.release_publisher, it))
                    }
                    release.releaseNotes?.takeIf { it.isNotBlank() }?.let {
                        append("\n")
                        append(getString(R.string.release_notes, it))
                    }
                    release.error?.let { append("\n").append(it) }
                }
                applyMigiText(14f)
                setLineSpacing(0f, 1.18f)
                setTextIsSelectable(true)
            })
            content.addGap(16)
            when (release.state) {
                ReleaseRepository.STATE_PENDING,
                ReleaseRepository.STATE_DISMISSED -> content.addView(primaryActionButton(R.string.download_release).apply {
                    setOnClickListener {
                        isEnabled = false
                        ReleaseInstaller(this@MainActivity).download(release.artifact.id) { result ->
                            runOnUiThread {
                                updateStatus(result.fold(
                                    onSuccess = { getString(R.string.release_downloaded) },
                                    onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
                                ))
                                refreshReleases()
                            }
                        }
                    }
                }, matchWidth())
                ReleaseRepository.STATE_DOWNLOADED,
                ReleaseRepository.STATE_INSTALLING,
                ReleaseRepository.STATE_FAILED -> content.addView(primaryActionButton(R.string.install_release).apply {
                    if (release.tempPath == null) {
                        setText(R.string.download_release)
                        setOnClickListener {
                            isEnabled = false
                            ReleaseInstaller(this@MainActivity).download(release.artifact.id) { result ->
                                runOnUiThread {
                                    updateStatus(result.fold(
                                        onSuccess = { getString(R.string.release_downloaded) },
                                        onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
                                    ))
                                    refreshReleases()
                                }
                            }
                        }
                        return@apply
                    }
                    setText(
                        if (release.artifact.packageName == BuildConfig.APPLICATION_ID) {
                            R.string.install_migi_update
                        } else {
                            R.string.install_release
                        },
                    )
                    setOnClickListener {
                        val installer = ReleaseInstaller(this@MainActivity)
                        if (!packageManager.canRequestPackageInstalls()) {
                            startActivity(installer.unknownSourcesSettingsIntent())
                            updateStatus(R.string.allow_unknown_source)
                            return@setOnClickListener
                        }
                        isEnabled = false
                        installer.install(this@MainActivity, release.artifact.id) { result ->
                            updateStatus(result.fold(
                                onSuccess = { getString(R.string.install_submitted) },
                                onFailure = { getString(R.string.release_failed, it.message ?: "unknown error") },
                            ))
                            refreshReleases()
                        }
                    }
                }, matchWidth())
            }
            val item = MaterialCardView(this).apply {
                applyMigiCard(radiusDp = 20)
                addView(content, matchWidth())
            }
            releaseList.addView(item, matchWidth().apply { bottomMargin = dp(10) })
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
        if (CredentialStore(this).load() == null) {
            updateStatus(R.string.device_not_paired)
            return
        }
        getSharedPreferences(PREFERENCES, MODE_PRIVATE).edit()
            .putString(KEY_ENDPOINT, value)
            .putString(KEY_CERTIFICATE_PIN, pin)
            .apply()
        startForegroundService(
            Intent(this, ConnectionService::class.java).setAction(ConnectionService.ACTION_RECONFIGURE),
        )
        updateStatus(R.string.service_starting)
    }

    private fun handlePairingIntent(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        val invitation = PairingInvitation.parse(intent.data) ?: run {
            setIntent(Intent(this, MainActivity::class.java))
            updateStatus(R.string.invalid_pairing_invitation)
            return
        }
        // The one-time secret must not be replayed after activity recreation.
        setIntent(Intent(this, MainActivity::class.java))
        MaterialAlertDialogBuilder(this)
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
        updateStatus(R.string.pairing_in_progress)
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
                PlaybackQueueRepository(this).reset()
                PlaybackMediaCache(this).clear()
            }
            runOnUiThread {
                result.onSuccess {
                    endpoint.setText(invitation.endpoint)
                    certificatePin.setText(invitation.pin)
                    startForegroundService(
                        Intent(this, ConnectionService::class.java)
                            .setAction(ConnectionService.ACTION_RECONFIGURE),
                    )
                    updateStatus(R.string.pairing_complete)
                    requestBatteryOptimizationExemption()
                }.onFailure {
                    updateStatus(getString(R.string.pairing_failed, it.message ?: "unknown error"))
                }
            }
        }
    }

    private fun matchWidth() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    )

    private fun equalWidth() = LinearLayout.LayoutParams(
        0,
        ViewGroup.LayoutParams.WRAP_CONTENT,
        1f,
    )

    private fun updateStatus(resourceID: Int) {
        updateStatus(getString(resourceID))
    }

    private fun updateStatus(message: CharSequence) {
        status.text = message
        if (::settingsStatus.isInitialized) settingsStatus.text = message
        if (::connectionDot.isInitialized) {
            connectionDot.background = roundedDrawable(
                if (ConnectionService.isRunning) MigiPalette.secondary else MigiPalette.muted,
                dp(6).toFloat(),
            )
        }
    }

    private data class PlaybackTrackRow(
        val card: MaterialCardView,
        val number: TextView,
        val title: TextView,
        val subtitle: TextView,
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
            const val KEY_CONNECTION_RECOVERY_ERROR = "connection_recovery_error"
            const val DEFAULT_AUDIO_VOLUME = 100
            const val EXTRA_OPEN_TAB = "dev.migi.app.extra.OPEN_TAB"
            const val TAB_MUSIC = 1
            private const val REQUEST_CHOOSE_FILE = 20
            private const val REQUEST_SAVE_FILE = 21
            private const val REQUEST_CHOOSE_PHOTO = 22
            private const val STATE_SELECTED_TAB = "selected_tab"
            private const val TAB_STATUS = 0
            private const val PLAYBACK_SEEK_MAX = 1_000
            private const val NAV_HOME = 10_001
            private const val NAV_MUSIC = 10_002
            private const val NAV_FILES = 10_003
            private const val NAV_UPDATES = 10_004
            private const val NAV_SETTINGS = 10_005
            private val NAVIGATION_IDS = intArrayOf(
                NAV_HOME,
                NAV_MUSIC,
                NAV_FILES,
                NAV_UPDATES,
                NAV_SETTINGS,
            )

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

internal object PickerResultPolicy {
    enum class Outcome {
        CANCELLED,
        MISSING_URI,
        SELECTED,
    }

    fun classify(succeeded: Boolean, hasUri: Boolean): Outcome = when {
        !succeeded -> Outcome.CANCELLED
        !hasUri -> Outcome.MISSING_URI
        else -> Outcome.SELECTED
    }
}
