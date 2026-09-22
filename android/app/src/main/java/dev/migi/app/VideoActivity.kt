package dev.migi.app

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.provider.OpenableColumns
import android.content.SharedPreferences
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.WindowManager
import android.view.WindowInsets
import android.view.View
import android.window.OnBackInvokedDispatcher
import android.widget.Button
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
    private var playerView: PlayerView? = null
    private var generation = 0
    private var lastActive: String? = null
    private val progress = mutableMapOf<PlaybackTrack, TextView>()
    private var downloadStatus: TextView? = null
    private val libraryListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == "entries") runOnUiThread { if (current == null) showLibrary() }
    }
    private val tick = object : Runnable {
        override fun run() {
            if (current == null && lastActive != VideoDownloads.active) showLibrary()
            lastActive = VideoDownloads.active
            for ((track, label) in progress) label.text = description(track)
            downloadStatus?.text = VideoDownloads.error ?: if (VideoDownloads.active != null)
                "Загрузка… Оставьте Migi открытым. При обрыве можно продолжить." else "Выберите серию. Скачанное доступно без интернета."
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
    private fun text(parent: LinearLayout, value: String, size: Float = 16f): TextView = TextView(this).apply {
        text = value; textSize = size; setPadding(20, 12, 20, 12); parent.addView(this)
    }
    private fun button(parent: LinearLayout, label: String, enabled: Boolean = true, action: () -> Unit) {
        parent.addView(Button(this).apply { text = label; isEnabled = enabled; setOnClickListener { action() } })
    }
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
        generation++; releasePlayer(); current = null; restoreID = null
        progress.clear(); root.removeAllViews()
        text(root, "Видео", 26f)
        val content = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        root.addView(ScrollView(this).apply { addView(content) }, LinearLayout.LayoutParams(-1, 0, 1f))
        downloadStatus = text(content, VideoDownloads.error ?: "Выберите серию. Скачанное доступно без интернета.")
        button(content, "Сериалы и фильмы на сервере") { loadCollections(content) }
        val entries = library.entries()
        if (entries.isEmpty()) text(content, "Попросите агента прислать сериал или фильм, либо выберите сохранённую подборку на сервере.")
        for ((name, episodes) in entries.groupBy { it.collection }) {
            text(content, name, 22f)
            for (entry in episodes) {
                val track = entry.track
                text(content, track.title, 18f)
                progress[track] = text(content, description(track), 14f)
                if (VideoDownloads.available(this, track)) {
                    button(content, if (library.position(track) > 0) "Продолжить" else "Смотреть") { play(track) }
                } else {
                    button(content, if (library.position(track) > 0) "Продолжить онлайн" else "Смотреть онлайн") { play(track) }
                    button(content, if (VideoDownloads.bytes(this, track) > 0) "Докачать" else "Скачать для офлайна", VideoDownloads.active == null) {
                        VideoDownloads.start(this, track); showLibrary()
                    }
                }
                if (VideoDownloads.bytes(this, track) > 0) button(content, "Удалить с телефона", VideoDownloads.active != track.id) {
                    runCatching { VideoDownloads.remove(this, track) }.onFailure { showError(it.message) }
                    showLibrary()
                }
            }
        }
    }
    private fun showError(message: String?) {
        AlertDialog.Builder(this).setMessage(message ?: "Ошибка").setPositiveButton("ОК", null).show()
    }
    private fun loadCollections(content: LinearLayout) {
        val request = ++generation
        val status = text(content, "Загрузка списка…")
        executor.execute {
            val result = runCatching { SavedPlaylistClient(applicationContext).list().filter { it.kind == "video" } }
            runOnUiThread {
                if (isDestroyed || generation != request) return@runOnUiThread
                result.onSuccess { collections ->
                    status.text = if (collections.isEmpty()) "Сохранённых видео пока нет" else "Выберите подборку:"
                    collections.forEach { collection ->
                        button(content, "${collection.name} · ${collection.trackCount}") {
                            status.text = "Получение списка серий…"
                            executor.execute {
                                val started = runCatching { SavedPlaylistClient(applicationContext).start(collection.id) }
                                runOnUiThread {
                                    if (!isDestroyed && generation == request) status.text = started.fold(
                                        { "Подборка запрошена. Список появится после получения события." },
                                        { "Не удалось получить подборку: ${it.message}" })
                                }
                            }
                        }
                    }
                }.onFailure { status.text = "Не удалось загрузить список: ${it.message}" }
            }
        }
    }
    private fun play(track: PlaybackTrack, autoplay: Boolean = true) {
        setFullscreen(false)
        val offline = VideoDownloads.available(this, track)
        generation++; releasePlayer(); current = track
        progress.clear(); downloadStatus = null; root.removeAllViews()
        button(root, "К списку видео") { showLibrary() }
        text(root, track.title, 18f)
        val status = text(root, if (offline) "С телефона" else "Подключение к видео…", 14f)
        val view = PlayerView(this).apply {
            setShowSubtitleButton(true)
            setShowBuffering(PlayerView.SHOW_BUFFERING_ALWAYS)
            setFullscreenButtonClickListener { setFullscreen(it) }
        }
        playerView = view
        root.addView(view, LinearLayout.LayoutParams(-1, 0, 1f))
        button(root, "Звук и субтитры") {
            val options = mutableListOf("Звуковая дорожка", "Субтитры", "Выбрать файл субтитров")
            if (library.subtitle(track) != null) options.add("Убрать внешний файл субтитров")
            AlertDialog.Builder(this).setItems(options.toTypedArray()) { _, which ->
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
                    AlertDialog.Builder(this@VideoActivity)
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
    private fun setFullscreen(enabled: Boolean) {
        fullscreen = enabled
        for (index in 0 until root.childCount) {
            val child = root.getChildAt(index)
            if (child !== playerView) child.visibility = if (enabled) View.GONE else View.VISIBLE
        }
        if (enabled) window.insetsController?.hide(WindowInsets.Type.systemBars())
        else window.insetsController?.show(WindowInsets.Type.systemBars())
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
        AlertDialog.Builder(this).setTitle(if (type == C.TRACK_TYPE_TEXT) "Субтитры" else "Звуковая дорожка")
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
