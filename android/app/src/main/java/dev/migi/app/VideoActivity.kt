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
    private var selectedCollection: String? = null
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
    private var phoneOnly = false
    private var refreshingCatalog = false
    private var loadingCollection: String? = null
    private var catalogNotice: String? = null
    private var collectionNotice: String? = null
    private var lastCatalogRefresh = 0L
    private val libraryListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key in setOf("entries", "catalog")) runOnUiThread { if (current == null) showLibrary() }
        if (key == "event") runOnUiThread { refreshCatalog() }
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
            if (current == null && android.os.SystemClock.elapsedRealtime() - lastCatalogRefresh > 30_000) refreshCatalog(force = false)
            savePosition(); handler.postDelayed(this, 1000)
        }
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        library = VideoLibrary(this)
        val previous = library.currentSession()
        restoreID = if (savedInstanceState == null) previous?.trackID else savedInstanceState.getString("video")
        selectedCollection = if (savedInstanceState == null) previous?.collectionID else savedInstanceState.getString("collection")
        phoneOnly = savedInstanceState?.getBoolean("phone-only") ?: false
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
            if (fullscreen) setFullscreen(false) else if (current != null) showLibrary() else leaveLibrary()
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
        restoreID = null; refreshCatalog(); handler.post(tick)
    }
    override fun onPause() { savePosition(); player?.pause(); super.onPause() }
    override fun onStop() { restoreID = current?.id; releasePlayer(); handler.removeCallbacks(tick); super.onStop() }
    override fun onSaveInstanceState(outState: Bundle) {
        savePosition(); outState.putString("video", current?.id ?: restoreID)
        outState.putString("collection", selectedCollection)
        outState.putBoolean("phone-only", phoneOnly)
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
        if (active.playbackState == Player.STATE_READY)
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
    private fun toolbar(title: String, action: () -> Unit): LinearLayout {
        val bar = LinearLayout(this).apply {
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(20), dp(12), dp(20), dp(12))
        }
        bar.addView(iconButton(R.drawable.ic_back, "Назад", action), LinearLayout.LayoutParams(dp(48), dp(48)))
        bar.addView(TextView(this).apply {
            text = title; applyMigiText(16f, weight = Typeface.BOLD)
        }, LinearLayout.LayoutParams(0, -2, 1f).apply { marginStart = dp(14) })
        root.addView(bar, width())
        return bar
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
        val percent = downloaded * 100 / track.size.coerceAtLeast(1)
        val state = when {
            VideoDownloads.available(this, track) -> "С телефона"
            VideoDownloads.active == track.id -> "Скачивается · $percent%"
            downloaded > 0 -> "Загружено $percent%"
            else -> "Онлайн"
        }
        val position = library.position(track) / 1000
        val viewed = when {
            library.watched(track) -> " · Просмотрено"
            position > 0 -> " · ${position / 60}:${(position % 60).toString().padStart(2, '0')}"
            else -> ""
        }
        return state + viewed
    }
    private fun showLibrary() {
        setFullscreen(false)
        generation++; releasePlayer(); current = null; restoreID = null; playerView = null
        progress.clear(); root.removeAllViews()
        val bar = toolbar("Видео") { leaveLibrary() }
        catalogButton = MaterialButton(this).apply {
            text = if (refreshingCatalog) "Загрузка…" else "Обновить"
            contentDescription = "Обновить видеотеку"
            isEnabled = !refreshingCatalog
            isAllCaps = false
            applyMigiText(14f, MigiPalette.primary, Typeface.BOLD)
            backgroundTintList = ColorStateList.valueOf(Color.TRANSPARENT)
            insetTop = 0; insetBottom = 0; cornerRadius = dp(16)
            minimumWidth = 0; minWidth = 0
            setPadding(dp(12), 0, dp(12), 0)
            setOnClickListener { refreshCatalog() }
            bar.addView(this, LinearLayout.LayoutParams(-2, dp(48)))
        }
        val content = column(20).apply { setPadding(dp(20), dp(8), dp(20), dp(20)) }
        root.addView(ScrollView(this).apply { isFillViewport = true; clipToPadding = false; addView(content) }, width(0).apply { weight = 1f })
        downloadStatus = text(content, "", 13f, MigiPalette.muted).apply {
            visibility = View.GONE; setPadding(0, 0, 0, dp(16))
        }
        val catalog = library.catalog()
        if (selectedCollection != null && selectedCollection != RECEIVED && catalog != null &&
            catalog.none { it.id == selectedCollection }) selectedCollection = null
        if (selectedCollection == null) {
            val tabs = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
            listOf("Все видео", "На телефоне").forEachIndexed { index, label ->
                tabs.addView(MaterialButton(this).apply {
                    text = label; isAllCaps = false; cornerRadius = dp(18)
                    val selected = phoneOnly == (index == 1)
                    backgroundTintList = ColorStateList.valueOf(if (selected) MigiPalette.primary else MigiPalette.surfaceHigh)
                    applyMigiText(14f, if (selected) MigiPalette.onPrimary else MigiPalette.muted, Typeface.BOLD)
                    setOnClickListener { phoneOnly = index == 1; showLibrary() }
                }, LinearLayout.LayoutParams(0, dp(48), 1f).apply { if (index > 0) marginStart = dp(8) })
            }
            content.addView(tabs, width())
            gap(content, 16)
        }
        val notice = if (selectedCollection == null) catalogNotice else collectionNotice
        if (!phoneOnly && notice != null) {
            text(content, notice, 13f, MigiPalette.muted); gap(content, 16)
        }
        if (selectedCollection == null && !phoneOnly) {
            library.currentTrack()?.let { last ->
                val resume = card(content)
                text(resume, if (library.watched(last) && library.position(last) == 0L) "Последний просмотр" else "Продолжить просмотр", 13f, MigiPalette.primary, true)
                gap(resume, 8)
                text(resume, library.currentSession()?.collectionName.orEmpty(), 16f, bold = true)
                text(resume, last.title, 14f, MigiPalette.muted).apply { maxLines = 2; ellipsize = TextUtils.TruncateAt.END }
                gap(resume, 6)
                text(resume, description(last), 12f, MigiPalette.muted)
                button(resume, "Открыть плеер", icon = R.drawable.ic_play) {
                    selectedCollection = library.currentSession()?.collectionID
                    play(last, false)
                }
            }
        }
        val received = library.received()
        if (selectedCollection == null && !phoneOnly) {
            val cards = VideoCatalogView.collections(catalog.orEmpty(), received)
            if (cards.isEmpty()) {
                val empty = card(content)
                text(empty, if (refreshingCatalog) "Загружаем видеотеку…" else "Что посмотрим?", 22f, bold = true)
                gap(empty, 10)
                text(empty, if (catalog == null) "Подключитесь к серверу, чтобы увидеть сериалы и фильмы. Скачанные видео — во вкладке «На телефоне»."
                    else "Попросите агента добавить сериал или фильм. Сохранённые плейлисты появятся здесь автоматически.", 15f, MigiPalette.muted)
            }
            for (collection in cards) {
                val name = collection.name
                val episodes = if (collection.id == RECEIVED) received else
                    library.playlist(collection.id)?.items.orEmpty().map { VideoEntry(it, name) }
                val body = LinearLayout(this).apply {
                    gravity = Gravity.CENTER_VERTICAL
                    minimumHeight = dp(88)
                    setPadding(dp(16), dp(16), dp(12), dp(16))
                }
                body.addView(ImageView(this).apply {
                    setImageResource(R.drawable.ic_video)
                    imageTintList = ColorStateList.valueOf(MigiPalette.primary)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                }, LinearLayout.LayoutParams(dp(24), dp(24)))
                val labels = column()
                text(labels, name, 16f, bold = true).apply { maxLines = 2; ellipsize = TextUtils.TruncateAt.END }
                gap(labels, 6)
                val downloaded = episodes.count { VideoDownloads.available(this, it.track) }
                text(labels, "${collection.count} видео" + if (downloaded > 0) " · Скачано: $downloaded" else "", 12f, MigiPalette.muted)
                body.addView(labels, LinearLayout.LayoutParams(0, -2, 1f).apply { marginStart = dp(16) })
                body.addView(ImageView(this).apply {
                    setImageResource(R.drawable.ic_back); rotation = 180f
                    imageTintList = ColorStateList.valueOf(MigiPalette.muted)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                }, LinearLayout.LayoutParams(dp(20), dp(20)).apply { marginStart = dp(12) })
                content.addView(MaterialCardView(this).apply {
                    applyMigiCard(radiusDp = 18, stroke = false)
                    isFocusable = true
                    addView(body)
                    setOnClickListener { openCollection(collection.id) }
                }, width().apply { bottomMargin = dp(12) })
            }
            return
        }
        val id = selectedCollection
        val name = when { phoneOnly -> "На телефоне"; id == RECEIVED -> "От агента"
            else -> catalog.orEmpty().find { it.id == id }?.name ?: library.playlist(id.orEmpty())?.name.orEmpty() }
        val episodes = when { phoneOnly -> VideoCatalogView.local(library.entries()) { VideoDownloads.bytes(this, it) > 0 }
            id == RECEIVED -> received
            else -> library.playlist(id.orEmpty())?.items.orEmpty().map { VideoEntry(it, name) } }
        run {
            if (episodes.isEmpty()) {
                val empty = card(content)
                text(empty, if (phoneOnly) "Здесь будут скачанные видео" else if (loadingCollection == id) "Загружаем серии…" else "Список серий пока не загружен", 20f, bold = true)
                gap(empty, 10)
                text(empty, if (phoneOnly) "В меню серии выберите «Скачать для офлайна»." else "Подключитесь к серверу и нажмите «Обновить».", 15f, MigiPalette.muted)
                return
            }
            text(content, name, 24f, bold = true)
            gap(content, 8)
            text(content, "${episodes.size} видео", 13f, MigiPalette.muted)
            gap(content, 20)
            val group = column()
            for ((index, entry) in episodes.withIndex()) {
                if (index > 0) group.addView(View(this).apply { setBackgroundColor(MigiPalette.outline) },
                    width(dp(1)).apply { marginStart = dp(52); marginEnd = dp(16) })
                val track = entry.track
                val row = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
                val open = LinearLayout(this).apply {
                    gravity = Gravity.CENTER_VERTICAL
                    minimumHeight = dp(76)
                    setPadding(dp(16), dp(12), 0, dp(12))
                    background = rippleDrawable(this@VideoActivity, Color.TRANSPARENT, 16)
                    isFocusable = true
                    setOnClickListener { play(track) }
                }
                open.addView(ImageView(this).apply {
                    setImageResource(R.drawable.ic_play)
                    imageTintList = ColorStateList.valueOf(MigiPalette.primary)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                }, LinearLayout.LayoutParams(dp(20), dp(20)))
                val labels = column()
                text(labels, track.title, 16f, bold = true).apply {
                    maxLines = 2; ellipsize = TextUtils.TruncateAt.END
                }
                gap(labels, 5)
                progress[track] = text(labels, description(track), 12f, MigiPalette.muted)
                open.addView(labels, LinearLayout.LayoutParams(0, -2, 1f).apply { marginStart = dp(16) })
                row.addView(open, LinearLayout.LayoutParams(0, -2, 1f))
                row.addView(iconButton(R.drawable.ic_more, "Действия: ${track.title}") { showVideoActions(track) }.apply {
                    background = rippleDrawable(this@VideoActivity, Color.TRANSPARENT, 24)
                }, LinearLayout.LayoutParams(dp(48), dp(48)).apply { marginEnd = dp(4) })
                group.addView(row, width())
            }
            content.addView(MaterialCardView(this).apply {
                applyMigiCard(radiusDp = 18, stroke = false)
                addView(group)
            }, width().apply { bottomMargin = dp(12) })
        }
    }
    private fun leaveLibrary() {
        if (selectedCollection != null) { selectedCollection = null; showLibrary() } else finish()
    }
    private fun showVideoActions(track: PlaybackTrack) {
        val actions = mutableListOf<Pair<String, () -> Unit>>()
        val available = VideoDownloads.available(this, track)
        val bytes = VideoDownloads.bytes(this, track)
        val playLabel = if (library.position(track) > 0) "Продолжить просмотр" else "Смотреть"
        actions.add((playLabel + if (available) "" else " онлайн") to { play(track) })
        val subtitleCache = PlaybackMediaCache(this, "video-sidecars/${track.sha256}", 32L shl 20)
        val missingSubtitles = track.subtitles.any { subtitleCache.cached(it) == null }
        if ((!available || missingSubtitles) && VideoDownloads.active == null) {
            actions.add((if (available) "Скачать субтитры для офлайна" else if (bytes > 0) "Продолжить скачивание" else "Скачать для офлайна") to {
                VideoDownloads.start(this, track); showLibrary()
            })
        }
        if (bytes > 0 && VideoDownloads.active != track.id) {
            actions.add("Удалить с телефона" to {
                dialog().setTitle("Удалить с телефона?")
                    .setMessage("Локальная копия будет удалена. Для повторного скачивания файл должен оставаться доступен на сервере.")
                    .setNegativeButton("Отмена", null).setPositiveButton("Удалить") { _, _ ->
                        runCatching { VideoDownloads.remove(this, track) }.onFailure { showError(it.message) }
                        showLibrary()
                    }.show()
            })
        }
        actions.add("Сведения о файле" to {
            dialog().setTitle(track.title)
                .setMessage("${description(track)}\n${track.size / (1024 * 1024)} МиБ")
                .setPositiveButton("Закрыть", null).show()
        })
        dialog().setTitle(track.title).setItems(actions.map { it.first }.toTypedArray()) { _, which ->
            actions[which].second()
        }.show()
    }
    private fun showError(message: String?) {
        dialog().setTitle("Не получилось").setMessage(message ?: "Попробуйте ещё раз")
            .setPositiveButton("ОК", null).show()
    }
    private fun connectionStamp(): List<String?> = listOf(
        DeviceIdentity.get(this),
        getSharedPreferences(MainActivity.PREFERENCES, MODE_PRIVATE).getString(MainActivity.KEY_ENDPOINT, null),
        getSharedPreferences(MainActivity.PREFERENCES, MODE_PRIVATE).getString(MainActivity.KEY_CERTIFICATE_PIN, null),
        CredentialStore(this).load(),
    )
    private fun refreshCatalog(force: Boolean = true) {
        if (refreshingCatalog || isDestroyed) return
        refreshingCatalog = true
        lastCatalogRefresh = android.os.SystemClock.elapsedRealtime()
        val identity = connectionStamp()
        catalogButton?.apply { isEnabled = false; text = "Загрузка…" }
        executor.execute {
            val result = runCatching { SavedPlaylistClient(applicationContext).list().filter { it.kind == "video" } }
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                refreshingCatalog = false
                if (identity != connectionStamp()) return@runOnUiThread
                val previousNotice = catalogNotice
                var changed = false
                result.onSuccess { collections ->
                    catalogNotice = null
                    changed = library.catalog() != collections
                    library.saveCatalog(collections)
                    if (force || changed) selectedCollection?.takeIf { it != RECEIVED && collections.any { p -> p.id == it } }
                        ?.let { loadCollection(it) }
                }.onFailure {
                    catalogNotice = if (library.catalog() == null) "Не удалось связаться с сервером."
                        else "Сервер недоступен · показан сохранённый каталог"
                }
                catalogButton?.apply { isEnabled = true; text = "Обновить" }
                if (current == null && (changed || catalogNotice != previousNotice)) showLibrary()
            }
        }
    }
    private fun openCollection(id: String) {
        selectedCollection = id
        collectionNotice = null
        if (id != RECEIVED) loadCollection(id)
        showLibrary()
    }
    private fun loadCollection(id: String) {
        if (loadingCollection == id || isDestroyed) return
        loadingCollection = id
        val identity = connectionStamp()
        executor.execute {
            val result = runCatching { SavedPlaylistClient(applicationContext).manifest(id) }
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                if (loadingCollection == id) loadingCollection = null
                if (identity != connectionStamp()) return@runOnUiThread
                val saved = result.mapCatching { library.savePlaylist(id, it) }
                if (selectedCollection == id) {
                    collectionNotice = if (saved.isSuccess) null else
                        if (library.playlist(id) == null) "Не удалось загрузить список серий. Попробуйте обновить."
                        else "Не удалось обновить серии · показана сохранённая версия"
                    if (current == null) showLibrary()
                }
            }
        }
    }
    companion object { private const val RECEIVED = "received" }
    private fun play(track: PlaybackTrack, autoplay: Boolean = true) {
        val offline = VideoDownloads.available(this, track)
        generation++; releasePlayer(); current = track
        library.remember(track, selectedCollection)
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
        val collectionName = library.currentSession()?.collectionName.orEmpty()
        text(details, collectionName, 12f, MigiPalette.primary, bold = true)
        gap(details, 10)
        text(details, track.title, 24f, bold = true)
        gap(details, 12)
        val status = text(details, description(track), 13f, MigiPalette.muted)
        if (!autoplay) {
            button(details, if (library.position(track) > 0) "Продолжить просмотр" else "Смотреть", primary = true, icon = R.drawable.ic_play) {
                player?.let {
                    if (it.playbackState == Player.STATE_ENDED) it.seekTo(0)
                    it.prepare(); it.play()
                }
            }
        }
        library.nextTrack()?.let { next ->
            button(details, "Следующая серия", icon = R.drawable.ic_next) {
                selectedCollection = library.currentSession()?.collectionID
                play(next)
            }
        }
        gap(details, 8)
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
        builder.setMediaSourceFactory(DefaultMediaSourceFactory(VideoStreamDataSource.factory(this, track))
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
                        Player.STATE_READY -> description(track)
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
            val subtitleItems = track.subtitles.map { subtitle ->
                MediaItem.SubtitleConfiguration.Builder(Uri.parse("migi-subtitle://${subtitle.id}"))
                    .setId("migi-sidecar-${subtitle.id}").setMimeType(subtitle.mime).setLabel(subtitle.label)
                    .setLanguage(subtitle.language.ifEmpty { null })
                    .setSelectionFlags(if (subtitle.default) C.SELECTION_FLAG_DEFAULT else 0).build()
            }.toMutableList()
            library.subtitle(track)?.let { (file, mime) ->
                subtitleItems.add(MediaItem.SubtitleConfiguration.Builder(Uri.fromFile(file))
                    .setId("migi-external-subtitle").setMimeType(mime).setLabel("Внешние субтитры")
                    .setSelectionFlags(C.SELECTION_FLAG_DEFAULT).build())
            }
            item.setSubtitleConfigurations(subtitleItems)
            active.setMediaItem(item.build())
            active.seekTo(library.position(track))
            if (autoplay) active.prepare()
            active.playWhenReady = autoplay
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
