package dev.migi.app

import android.app.Activity
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import android.content.Intent
import android.content.res.ColorStateList
import android.content.res.Configuration
import android.graphics.Color
import android.graphics.Typeface
import android.text.TextUtils
import android.view.Gravity
import android.widget.ImageButton
import android.widget.ImageView
import android.widget.Toast
import com.google.android.material.button.MaterialButton
import com.google.android.material.card.MaterialCardView
import android.content.pm.ActivityInfo
import android.provider.OpenableColumns
import android.content.SharedPreferences
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.WindowManager
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.view.View
import android.window.OnBackInvokedDispatcher
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.Tracks
import androidx.media3.common.util.UnstableApi
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.exoplayer.upstream.DefaultLoadErrorHandlingPolicy
import androidx.media3.ui.PlayerView
import java.util.concurrent.Executors

@UnstableApi
class VideoActivity : Activity() {
    private lateinit var root: LinearLayout
    private lateinit var library: VideoLibrary
    private var player: ExoPlayer? = null
    private var current: PlaybackTrack? = null
    private var restoreID: String? = null
    private val handler = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private var pendingSubtitleID: String? = null
    private var fullscreen = false
    private var orientationBeforeFullscreen = ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
    private var playerView: PlayerView? = null
    private var generation = 0
    private var lastActive: String? = null
    private val progress = mutableMapOf<PlaybackTrack, TextView>()
    private var downloadStatus: TextView? = null
    private var catalogButton: MaterialButton? = null
    private val libraryListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == "entries") runOnUiThread { if (current == null) showLibrary() }
    }
    private val tick = object : Runnable {
        override fun run() {
            if (current == null && lastActive != VideoDownloads.active) showLibrary()
            lastActive = VideoDownloads.active
            for ((track, label) in progress) label.text = description(track)
            downloadStatus?.let { label ->
                val message = VideoDownloads.error ?: if (VideoDownloads.active != null)
                    "Скачиваем видео. Оставьте Migi открытым — после обрыва загрузку можно продолжить." else ""
                label.text = message
                label.visibility = if (message.isEmpty()) View.GONE else View.VISIBLE
                label.setTextColor(if (VideoDownloads.error != null) MigiPalette.danger else MigiPalette.muted)
            }
            if (player?.let { it.playWhenReady && it.playbackState in listOf(Player.STATE_BUFFERING, Player.STATE_READY) } == true || VideoDownloads.active != null) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            savePosition(); handler.postDelayed(this, 1000)
        }
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        library = VideoLibrary(this)
        restoreID = savedInstanceState?.getString("video")
        pendingSubtitleID = savedInstanceState?.getString("subtitle-picker")
        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(MigiPalette.background)
            setOnApplyWindowInsetsListener { view, insets ->
                val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars())
                view.setPadding(bars.left, bars.top, bars.right, bars.bottom); insets
            }
        }
        setContentView(root)
        onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) {
            if (fullscreen) setFullscreen(false) else if (current != null) showLibrary() else finish()
        }
        getSharedPreferences("video-library", MODE_PRIVATE).registerOnSharedPreferenceChangeListener(libraryListener)
        val restored = restoreID
        showLibrary()
        restoreID = restored
        fullscreen = savedInstanceState?.getBoolean("fullscreen") ?: false
        orientationBeforeFullscreen = savedInstanceState?.getInt("previous-orientation",
            ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED) ?: ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED
    }
    override fun onStart() {
        super.onStart()
        restoreID?.let { id -> library.entries().find { it.track.id == id }?.track?.let { play(it, false) } }
        restoreID = null; handler.post(tick)
    }
    override fun onPause() { savePosition(); player?.pause(); super.onPause() }
    override fun onStop() { restoreID = current?.id; releasePlayer(); handler.removeCallbacks(tick); super.onStop() }
    override fun onSaveInstanceState(outState: Bundle) {
        savePosition(); outState.putString("video", current?.id ?: restoreID)
        outState.putBoolean("fullscreen", fullscreen)
        outState.putInt("previous-orientation", orientationBeforeFullscreen)
        outState.putString("subtitle-picker", pendingSubtitleID); super.onSaveInstanceState(outState)
    }
    override fun onDestroy() {
        generation++; executor.shutdownNow()
        getSharedPreferences("video-library", MODE_PRIVATE).unregisterOnSharedPreferenceChangeListener(libraryListener)
        super.onDestroy()
    }
    private fun savePosition() {
        val track = current ?: return
        val active = player ?: return
        if (active.playbackState != Player.STATE_IDLE && active.playbackState != Player.STATE_ENDED)
            library.savePosition(track, active.currentPosition)
    }
    private fun releasePlayer() { savePosition(); player?.release(); player = null }
    private fun width(height: Int = -2) = LinearLayout.LayoutParams(-1, height)
    private fun column(padding: Int = 0) = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(padding), dp(padding), dp(padding), dp(padding))
    }
    private fun gap(parent: LinearLayout, size: Int) { parent.addView(View(this), width(dp(size))) }
    private fun text(parent: LinearLayout, value: String, size: Float = 16f,
        color: Int = MigiPalette.text, bold: Boolean = false): TextView = TextView(this).apply {
        text = value
        applyMigiText(size, color, if (bold) Typeface.BOLD else Typeface.NORMAL)
        setLineSpacing(0f, 1.12f)
        parent.addView(this, width())
    }
    private fun button(parent: LinearLayout, label: String, enabled: Boolean = true,
        primary: Boolean = false, icon: Int = 0, action: () -> Unit): MaterialButton = MaterialButton(this).apply {
        text = label; isAllCaps = false; isEnabled = enabled
        applyMigiText(14f, if (primary) MigiPalette.onPrimary else MigiPalette.text, Typeface.BOLD)
        minimumHeight = dp(50); minHeight = dp(50)
        insetTop = 0; insetBottom = 0; cornerRadius = dp(16)
        backgroundTintList = ColorStateList.valueOf(if (primary) MigiPalette.primary else MigiPalette.surfaceHigh)
        strokeWidth = if (primary) 0 else dp(1)
        strokeColor = ColorStateList.valueOf(MigiPalette.outline)
        if (icon != 0) {
            setIconResource(icon); iconSize = dp(20); iconPadding = dp(8)
            iconGravity = MaterialButton.ICON_GRAVITY_TEXT_START
            iconTint = ColorStateList.valueOf(if (primary) MigiPalette.onPrimary else MigiPalette.primary)
        }
        alpha = if (enabled) 1f else 0.45f
        setOnClickListener { action() }
        parent.addView(this, width().apply { topMargin = dp(10) })
    }
    private fun iconButton(icon: Int, label: String, action: () -> Unit) = ImageButton(this).apply {
        setImageResource(icon); contentDescription = label
        imageTintList = ColorStateList.valueOf(MigiPalette.muted)
        background = rippleDrawable(this@VideoActivity, MigiPalette.surfaceHigh, 24)
        setPadding(dp(12), dp(12), dp(12), dp(12))
        setOnClickListener { action() }
    }
    private fun toolbar(title: String, action: () -> Unit) {
        val bar = LinearLayout(this).apply {
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(20), dp(12), dp(20), dp(12))
        }
        bar.addView(iconButton(R.drawable.ic_back, "Назад", action), LinearLayout.LayoutParams(dp(48), dp(48)))
        bar.addView(TextView(this).apply {
            text = title; applyMigiText(16f, weight = Typeface.BOLD)
        }, LinearLayout.LayoutParams(0, -2, 1f).apply { marginStart = dp(14) })
        root.addView(bar, width())
    }
    private fun card(parent: LinearLayout): LinearLayout {
        val body = column(18)
        parent.addView(MaterialCardView(this).apply {
            applyMigiCard(radiusDp = 22)
            addView(body)
        }, width().apply { bottomMargin = dp(12) })
        return body
    }
    private fun dialog() = MaterialAlertDialogBuilder(this)
        .setBackground(roundedDrawable(MigiPalette.surface, dp(28).toFloat(), MigiPalette.outline, dp(1)))
    private fun description(track: PlaybackTrack): String {
        val downloaded = VideoDownloads.bytes(this, track)
        val state = when {
            VideoDownloads.available(this, track) -> "Скачано"
            VideoDownloads.active == track.id -> "Загрузка: ${downloaded * 100 / track.size}%"
            downloaded > 0 -> "Загружено: ${downloaded * 100 / track.size}%"
            else -> "На сервере"
        }
        val viewed = if (library.watched(track)) " · Просмотрено" else ""
        val position = library.position(track) / 1000
        val resume = if (position > 0) " · ${position / 60}:${(position % 60).toString().padStart(2, '0')}" else ""
        return "$state · ${track.size / (1024 * 1024)} МиБ$viewed$resume"
    }
    private fun showLibrary() {
        setFullscreen(false)
        generation++; releasePlayer(); current = null; restoreID = null; playerView = null
        progress.clear(); root.removeAllViews()
        toolbar("Медиатека") { finish() }
        val content = column(20)
        root.addView(ScrollView(this).apply { isFillViewport = true; clipToPadding = false; addView(content) }, width(0).apply { weight = 1f })
        text(content, "Видео", 34f, bold = true).letterSpacing = -0.035f
        gap(content, 8)
        text(content, "Сериалы и фильмы. Онлайн и с собой.", 15f, MigiPalette.muted)
        gap(content, 12)
        catalogButton = button(content, "Сериалы и фильмы на сервере", icon = R.drawable.ic_video) { loadCollections() }
        downloadStatus = text(content, "", 13f, MigiPalette.muted).apply {
            visibility = View.GONE; setPadding(0, dp(12), 0, 0)
        }
        gap(content, 24)
        val entries = library.entries()
        if (entries.isEmpty()) {
            val empty = card(content)
            text(empty, "Что посмотрим?", 22f, bold = true)
            gap(empty, 10)
            text(empty, "Выберите подборку на сервере или попросите агента прислать сериал. Серии появятся здесь.", 15f, MigiPalette.muted)
        }
        for ((name, episodes) in entries.groupBy { it.collection }) {
            text(content, name, 20f, bold = true)
            gap(content, 12)
            for (entry in episodes) {
                val track = entry.track
                val body = card(content)
                val row = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
                row.addView(ImageView(this).apply {
                    setImageResource(R.drawable.ic_video)
                    imageTintList = ColorStateList.valueOf(MigiPalette.primary)
                    background = roundedDrawable(MigiPalette.surfaceHigh, dp(14).toFloat())
                    setPadding(dp(12), dp(12), dp(12), dp(12))
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                }, LinearLayout.LayoutParams(dp(48), dp(48)))
                row.addView(TextView(this).apply {
                    text = track.title; applyMigiText(16f, weight = Typeface.BOLD)
                    maxLines = 3; ellipsize = TextUtils.TruncateAt.END
                }, LinearLayout.LayoutParams(0, -2, 1f).apply { marginStart = dp(12) })
                if (VideoDownloads.bytes(this, track) > 0) {
                    row.addView(iconButton(R.drawable.ic_delete, "Удалить с телефона: ${track.title}") {
                        dialog().setTitle("Удалить с телефона?")
                            .setMessage("Видео останется на сервере. Его можно будет посмотреть онлайн или скачать снова.")
                            .setNegativeButton("Отмена", null).setPositiveButton("Удалить") { _, _ ->
                                runCatching { VideoDownloads.remove(this, track) }.onFailure { showError(it.message) }
                                showLibrary()
                            }.show()
                    }.apply { isEnabled = VideoDownloads.active != track.id; alpha = if (isEnabled) 1f else 0.4f },
                        LinearLayout.LayoutParams(dp(48), dp(48)).apply { marginStart = dp(6) })
                }
                body.addView(row, width())
                gap(body, 12)
                progress[track] = text(body, description(track), 13f, MigiPalette.muted)
                if (VideoDownloads.available(this, track)) {
                    button(body, if (library.position(track) > 0) "Продолжить" else "Смотреть", primary = true, icon = R.drawable.ic_play) { play(track) }
                } else {
                    button(body, if (library.position(track) > 0) "Продолжить онлайн" else "Смотреть онлайн", primary = true, icon = R.drawable.ic_play) { play(track) }
                    button(body, if (VideoDownloads.bytes(this, track) > 0) "Докачать" else "Скачать для офлайна",
                        enabled = VideoDownloads.active == null, icon = R.drawable.ic_nav_updates) {
                        VideoDownloads.start(this, track); showLibrary()
                    }
                }
            }
            gap(content, 12)
        }
    }
    private fun showError(message: String?) {
        dialog().setTitle("Не получилось").setMessage(message ?: "Попробуйте ещё раз")
            .setPositiveButton("ОК", null).show()
    }
    private fun loadCollections() {
        val request = ++generation
        catalogButton?.apply { isEnabled = false; text = "Загрузка подборок…" }
        executor.execute {
            val result = runCatching { SavedPlaylistClient(applicationContext).list().filter { it.kind == "video" } }
            runOnUiThread {
                if (isDestroyed || generation != request) return@runOnUiThread
                catalogButton?.apply { isEnabled = true; text = "Сериалы и фильмы на сервере" }
                result.onSuccess { collections ->
                    if (collections.isEmpty()) {
                        dialog().setTitle("На сервере пока нет видео")
                            .setMessage("Попросите агента проиндексировать сериал или фильм.").setPositiveButton("Понятно", null).show()
                    } else dialog().setTitle("Сериалы и фильмы")
                        .setItems(collections.map { "${it.name} · ${it.trackCount}" }.toTypedArray()) { _, which ->
                            executor.execute {
                                val started = runCatching { SavedPlaylistClient(applicationContext).start(collections[which].id) }
                                runOnUiThread {
                                    if (!isDestroyed && generation == request) started.onSuccess {
                                        Toast.makeText(this, "Обновляем видеотеку…", Toast.LENGTH_SHORT).show()
                                    }.onFailure { showError(it.message) }
                                }
                            }
                        }.setNegativeButton("Закрыть", null).show()
                }.onFailure { showError(it.message) }
            }
        }
    }
    private fun play(track: PlaybackTrack, autoplay: Boolean = true) {
        val offline = VideoDownloads.available(this, track)
        generation++; releasePlayer(); current = track
        progress.clear(); downloadStatus = null; root.removeAllViews()
        toolbar("Видеотека") { showLibrary() }
        val view = (layoutInflater.inflate(R.layout.migi_video_player, root, false) as PlayerView).apply {
            setShowSubtitleButton(true)
            setShowBuffering(PlayerView.SHOW_BUFFERING_ALWAYS)
            setFullscreenButtonClickListener { setFullscreen(it) }
        }
        playerView = view
        stylePlayer(view)
        root.addView(view, width())
        val details = column(20)
        root.addView(ScrollView(this).apply { addView(details); isFillViewport = true }, width(0).apply { weight = 1f })
        text(details, library.entries().firstOrNull { it.track.id == track.id }?.collection.orEmpty(), 12f, MigiPalette.primary, bold = true)
        gap(details, 10)
        text(details, track.title, 24f, bold = true)
        gap(details, 12)
        val status = text(details, if (offline) "С телефона" else "Подключение к видео…", 13f, MigiPalette.muted)
        gap(details, 18)
        button(details, "Полный экран", primary = true, icon = R.drawable.ic_fullscreen) { setFullscreen(true) }
        button(details, "Звук и субтитры", icon = R.drawable.ic_subtitles) {
            val options = mutableListOf("Звуковая дорожка", "Субтитры", "Выбрать файл субтитров")
            if (library.subtitle(track) != null) options.add("Убрать внешний файл субтитров")
            dialog().setItems(options.toTypedArray()) { _, which ->
                when (which) {
                    0 -> chooseTracks(C.TRACK_TYPE_AUDIO)
                    1 -> chooseTracks(C.TRACK_TYPE_TEXT)
                    2 -> {
                        pendingSubtitleID = track.id
                        savePosition()
                        startActivityForResult(Intent(Intent.ACTION_OPEN_DOCUMENT).apply {
                            type = "*/*"; addCategory(Intent.CATEGORY_OPENABLE)
                        }, 41)
                    }
                    3 -> { savePosition(); library.removeSubtitle(track); play(track, false) }
                }
            }.show()
        }
        setFullscreen(fullscreen)
        val builder = ExoPlayer.Builder(this)
            .setLoadControl(DefaultLoadControl.Builder().setBufferDurationsMs(15_000, 45_000, 3_000, 5_000).build())
        if (!offline) builder.setMediaSourceFactory(DefaultMediaSourceFactory(VideoStreamDataSource.factory(this, track))
            .setLoadErrorHandlingPolicy(DefaultLoadErrorHandlingPolicy(10)))
        player = builder.build().also { active ->
            active.trackSelectionParameters = active.trackSelectionParameters.buildUpon().setPreferredTextLanguage("ru").build()
            active.setAudioAttributes(AudioAttributes.Builder().setUsage(C.USAGE_MEDIA)
                .setContentType(C.AUDIO_CONTENT_TYPE_MOVIE).build(), true)
            active.setHandleAudioBecomingNoisy(true)
            view.player = active
            var externalSelected = false
            active.addListener(object : Player.Listener {
                override fun onTracksChanged(tracks: Tracks) {
                    // MergingMediaSource prefixes format IDs with its child source index.
                    // Select the attached file once, without fighting later user choices.
                    if (!externalSelected) {
                        for (group in tracks.groups.filter { it.type == C.TRACK_TYPE_TEXT }) {
                            val index = (0 until group.length).firstOrNull {
                                group.getTrackFormat(it).id?.substringAfterLast(':') == "migi-external-subtitle" && group.isTrackSupported(it)
                            } ?: continue
                            externalSelected = true
                            active.trackSelectionParameters = active.trackSelectionParameters.buildUpon()
                                .setTrackTypeDisabled(C.TRACK_TYPE_TEXT, false)
                                .setOverrideForType(TrackSelectionOverride(group.mediaTrackGroup, index)).build()
                            break
                        }
                    }
                }
                override fun onPlaybackStateChanged(state: Int) {
                    status.text = when (state) {
                        Player.STATE_BUFFERING -> "Буферизация…"
                        Player.STATE_READY -> if (offline) "С телефона" else "Просмотр онлайн"
                        Player.STATE_ENDED -> "Просмотр завершён"
                        else -> "Подключение к видео…"
                    }
                    if (state == Player.STATE_READY) android.util.Log.i("MigiVideo", "Player ready ${track.id}: position=${active.currentPosition} offline=$offline")
                    if (state == Player.STATE_ENDED) library.savePosition(track, 0, ended = true)
                }
                override fun onPlayerError(error: PlaybackException) {
                    status.text = "Воспроизведение остановлено"
                    dialog()
                        .setMessage("Не удалось воспроизвести видео: ${error.errorCodeName}")
                        .setPositiveButton("Повторить") { _, _ -> active.prepare(); active.play() }
                        .setNegativeButton("Закрыть", null).show()
                }
            })
            val item = MediaItem.Builder().setUri(if (offline) Uri.fromFile(VideoDownloads.file(this, track)) else Uri.parse("migi://video/${track.id}"))
                .setMimeType(track.mime)
            library.subtitle(track)?.let { (file, mime) ->
                item.setSubtitleConfigurations(listOf(MediaItem.SubtitleConfiguration.Builder(Uri.fromFile(file))
                    .setId("migi-external-subtitle").setMimeType(mime).setLabel("Внешние субтитры")
                    .setSelectionFlags(C.SELECTION_FLAG_DEFAULT).build()))
            }
            active.setMediaItem(item.build())
            active.seekTo(library.position(track)); active.prepare(); active.playWhenReady = autoplay
        }
    }
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != 41) return
        val id = pendingSubtitleID.also { pendingSubtitleID = null } ?: return
        if (resultCode != RESULT_OK) return
        val uri = data?.data ?: return
        val track = library.entries().find { it.track.id == id }?.track ?: return
        executor.execute {
            val result = runCatching {
                val name = contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use {
                    if (it.moveToFirst()) it.getString(0) else null
                } ?: ""
                val mime = VideoSubtitleFile.mimeForName(name)
                contentResolver.openInputStream(uri)?.use { library.installSubtitle(track, it, mime) }
                    ?: error("Не удалось открыть субтитры")
            }
            runOnUiThread {
                if (!isDestroyed) result.onSuccess { play(track, false) }.onFailure { showError(it.message) }
            }
        }
    }
    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        updatePlayerLayout()
    }
    private fun updatePlayerLayout() {
        val view = playerView ?: return
        root.setBackgroundColor(if (fullscreen) Color.BLACK else MigiPalette.background)
        view.background = roundedDrawable(Color.BLACK, if (fullscreen) 0f else dp(20).toFloat())
        view.clipToOutline = !fullscreen
        view.layoutParams = if (fullscreen) width(0).apply { weight = 1f } else {
            val availableWidth = resources.displayMetrics.widthPixels - dp(40)
            val height = minOf(availableWidth * 9 / 16, resources.displayMetrics.heightPixels / 2)
            width(height).apply { marginStart = dp(20); marginEnd = dp(20) }
        }
    }
    private fun stylePlayer(view: PlayerView) {
        view.setShowPreviousButton(false); view.setShowNextButton(false)
        view.setControllerAnimationEnabled(false)
        val play = view.findViewById<ImageButton>(androidx.media3.ui.R.id.exo_play_pause)
        play?.apply {
            background = rippleDrawable(this@VideoActivity, MigiPalette.primary, 32)
            imageTintList = ColorStateList.valueOf(MigiPalette.onPrimary)
            setPadding(dp(18), dp(18), dp(18), dp(18))
            layoutParams = layoutParams.apply { width = dp(64); height = dp(64) }
        }
        for (id in listOf(androidx.media3.ui.R.id.exo_position, androidx.media3.ui.R.id.exo_duration)) {
            view.findViewById<TextView>(id)?.applyMigiText(12f, MigiPalette.text)
        }
        view.findViewById<android.widget.ProgressBar>(androidx.media3.ui.R.id.exo_buffering)
            ?.indeterminateTintList = ColorStateList.valueOf(MigiPalette.primary)
    }
    private fun setFullscreen(enabled: Boolean) {
        if (enabled && !fullscreen) orientationBeforeFullscreen = requestedOrientation
        fullscreen = enabled
        // VideoActivity handles orientation changes in place, preserving the
        // player, its buffer, selected tracks and play/pause state.
        requestedOrientation = if (enabled) ActivityInfo.SCREEN_ORIENTATION_SENSOR_LANDSCAPE
            else orientationBeforeFullscreen
        playerView?.setFullscreenButtonState(enabled)
        for (index in 0 until root.childCount) {
            val child = root.getChildAt(index)
            if (child !== playerView) child.visibility = if (enabled) View.GONE else View.VISIBLE
        }
        updatePlayerLayout()
        window.insetsController?.let { controller ->
            controller.systemBarsBehavior = if (enabled)
                WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
                else WindowInsetsController.BEHAVIOR_DEFAULT
            if (enabled) controller.hide(WindowInsets.Type.systemBars())
            else controller.show(WindowInsets.Type.systemBars())
        }
    }
    private fun chooseTracks(type: Int) {
        val active = player ?: return
        val choices = active.currentTracks.groups.filter { it.type == type }.flatMap { group ->
            (0 until group.length).filter { group.isTrackSupported(it) }.map { index -> group to index }
        }
        val labels = listOf(if (type == C.TRACK_TYPE_TEXT) "Выключить" else "Автоматически") + choices.map { (group, index) ->
            val format = group.getTrackFormat(index)
            (if (group.isTrackSelected(index)) "✓ " else "") + listOfNotNull(format.label, format.language).distinct().joinToString(" · ").ifBlank { "Дорожка ${index + 1}" }
        }
        dialog().setTitle(if (type == C.TRACK_TYPE_TEXT) "Субтитры" else "Звуковая дорожка")
            .setItems(labels.toTypedArray()) { _, selected ->
                val parameters = active.trackSelectionParameters.buildUpon().clearOverridesOfType(type)
                    .setTrackTypeDisabled(type, type == C.TRACK_TYPE_TEXT && selected == 0)
                if (selected > 0) {
                    val (group, index) = choices[selected - 1]
                    parameters.setOverrideForType(TrackSelectionOverride(group.mediaTrackGroup, index))
                }
                active.trackSelectionParameters = parameters.build()
            }.show()
    }
}
