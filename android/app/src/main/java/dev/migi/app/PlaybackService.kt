package dev.migi.app

import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.util.Log
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.FileDataSource
import androidx.media3.datasource.ResolvingDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.session.MediaController
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import androidx.media3.session.SessionToken
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicLong

class PlaybackService : MediaSessionService() {
	private lateinit var player: ExoPlayer
	private lateinit var mediaSession: MediaSession
	private val mainHandler = Handler(Looper.getMainLooper())
	private val replacementExecutor = Executors.newFixedThreadPool(2)
	private val replacementGeneration = AtomicLong()

	@UnstableApi
	override fun onCreate() {
		super.onCreate()
		val application = applicationContext
		val cache = PlaybackMediaCache(application)
		val resolvingDataSource = ResolvingDataSource.Factory(FileDataSource.Factory()) { dataSpec ->
			val uri = dataSpec.uri
			require(uri.scheme == MEDIA_SCHEME) { "Unsupported playback URI" }
			val eventID = uri.authority?.toLongOrNull() ?: error("Playback event ID is missing")
			val trackID = uri.lastPathSegment ?: error("Playback media ID is missing")
			val queue = requireNotNull(activeQueue) {
				"No active playback queue is available"
			}
			require(queue.eventID == eventID) { "Track belongs to another playback queue" }
			val track = queue.items.firstOrNull { it.id == trackID }
				?: error("Track is not present in the playback queue")
			val file = cache.prepare(track)
			dataSpec.withUri(Uri.fromFile(file))
		}
		val mediaSourceFactory = DefaultMediaSourceFactory(this)
			.setDataSourceFactory(resolvingDataSource)
		player = ExoPlayer.Builder(this)
			.setMediaSourceFactory(mediaSourceFactory)
			.build().apply {
			setAudioAttributes(
				AudioAttributes.Builder()
					.setUsage(C.USAGE_MEDIA)
					.setContentType(C.AUDIO_CONTENT_TYPE_MUSIC)
					.build(),
				true,
			)
			setHandleAudioBecomingNoisy(true)
		}
		val activity = PendingIntent.getActivity(
			this,
			PLAYBACK_ACTIVITY_REQUEST,
			Intent(this, MainActivity::class.java).putExtra(MainActivity.EXTRA_OPEN_TAB, MainActivity.TAB_MUSIC),
			PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
		)
		mediaSession = MediaSession.Builder(this, player)
			.setSessionActivity(activity)
			.build()
		instance = this
	}

	override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession = mediaSession

	override fun onDestroy() {
		replacementGeneration.incrementAndGet()
		replacementExecutor.shutdownNow()
		if (instance === this) instance = null
		mediaSession.release()
		player.release()
		activeQueue = null
		clearHotSwapState(applicationContext)
		super.onDestroy()
	}

	private fun requestHotReplacement(eventID: Long) {
		mainHandler.post {
			val application = applicationContext
			val preferences = application.getSharedPreferences(
				MainActivity.PREFERENCES,
				Context.MODE_PRIVATE,
			)
			val queue = PlaybackQueueRepository(application).current() ?: return@post
			val activeEventID = activeQueue?.eventID
			if (
				queue.eventID != eventID ||
				player.mediaItemCount == 0 ||
				!PlaybackHotSwapPolicy.shouldReplace(
					preferences.getBoolean(MainActivity.KEY_PLAYBACK_HOT_SWAP, false),
					activeEventID,
					eventID,
				)
			) {
				return@post
			}

			val generation = replacementGeneration.incrementAndGet()
			updateHotSwapState(application, eventID, HOT_SWAP_STATE_PREPARING)
			Log.i(TAG, "Preparing queue $eventID for hot replacement")
			runCatching {
				replacementExecutor.execute {
					if (generation != replacementGeneration.get()) return@execute
					val result = runCatching {
						val latest = requireNotNull(PlaybackQueueRepository(application).current())
						require(latest.eventID == eventID) { "A newer playback queue is available" }
						require(
							preferences.getBoolean(MainActivity.KEY_PLAYBACK_HOT_SWAP, false),
						) { "Hot playlist replacement was disabled" }
						val cache = PlaybackMediaCache(application)
						cache.prepare(latest.items.first())
						latest.artwork?.let { artwork ->
							runCatching { cache.prepare(artwork) }
								.onFailure { Log.w(TAG, "Could not prefetch artwork for queue $eventID", it) }
						}
						latest
					}
					mainHandler.post {
						finishHotReplacement(eventID, generation, result)
					}
				}
			}.onFailure { error ->
				updateHotSwapState(application, eventID, HOT_SWAP_STATE_FAILED)
				Log.e(TAG, "Could not schedule hot replacement for queue $eventID", error)
			}
		}
	}

	private fun finishHotReplacement(
		eventID: Long,
		generation: Long,
		prepared: Result<PlaybackQueue>,
	) {
		if (generation != replacementGeneration.get() || instance !== this) return
		val application = applicationContext
		val preferences = application.getSharedPreferences(
			MainActivity.PREFERENCES,
			Context.MODE_PRIVATE,
		)
		val latest = PlaybackQueueRepository(application).current()
		if (!PlaybackHotSwapPolicy.canCommit(
			preferences.getBoolean(MainActivity.KEY_PLAYBACK_HOT_SWAP, false),
			activeQueue?.eventID,
			latest?.eventID,
			eventID,
			player.mediaItemCount > 0,
		)) {
			clearHotSwapState(application)
			return
		}

		prepared.mapCatching { queue ->
			val items = mediaItems(application, queue)
			val continuePlaying = player.playWhenReady
			val previousQueue = activeQueue
			activeQueue = queue
			runCatching {
				player.setMediaItems(items)
				player.prepare()
				player.playWhenReady = continuePlaying
			}.onFailure {
				activeQueue = previousQueue
			}.getOrThrow()
			queue
		}.onSuccess { queue ->
			updateHotSwapState(application, queue.eventID, HOT_SWAP_STATE_COMPLETE)
			Log.i(TAG, "Hot-replaced playback with queue ${queue.eventID}")
		}.onFailure { error ->
			updateHotSwapState(application, eventID, HOT_SWAP_STATE_FAILED)
			Log.e(TAG, "Failed to hot-replace playback with queue $eventID", error)
		}
	}

	companion object {
		private const val PLAYBACK_ACTIVITY_REQUEST = 41
		private const val TAG = "MigiPlayback"
		private const val MEDIA_SCHEME = "migi-media"
		const val HOT_SWAP_STATE_PREPARING = "preparing"
		const val HOT_SWAP_STATE_COMPLETE = "complete"
		const val HOT_SWAP_STATE_FAILED = "failed"
		@Volatile private var instance: PlaybackService? = null
		@Volatile private var activeQueue: PlaybackQueue? = null

		fun start(context: Context, eventID: Long, onComplete: (Throwable?) -> Unit) {
			val application = context.applicationContext
			cancelPendingReplacement(application)
			val token = SessionToken(application, ComponentName(application, PlaybackService::class.java))
			val controllerFuture = MediaController.Builder(application, token).buildAsync()
			controllerFuture.addListener({
				val result = runCatching {
					val queue = requireNotNull(PlaybackQueueRepository(application).current()) {
						"No playback queue is available"
					}
					require(queue.eventID == eventID) { "Playback queue changed before it could start" }
					val items = mediaItems(application, queue)
					val controller = controllerFuture.get()
					val previousQueue = activeQueue
					activeQueue = queue
					runCatching {
						controller.setMediaItems(items)
						controller.prepare()
						controller.play()
					}.onFailure {
						activeQueue = previousQueue
					}.getOrThrow()
					Log.i(TAG, "Started queue $eventID with ${items.size} tracks through MediaController")
				}
				MediaController.releaseFuture(controllerFuture)
				val error = result.exceptionOrNull()?.let { it.cause ?: it }
				if (error != null) Log.e(TAG, "Failed to start playback queue $eventID", error)
				onComplete(error)
			}, context.mainExecutor)
		}

		fun replaceWhenReady(context: Context, eventID: Long) {
			val application = context.applicationContext
			val enabled = application.getSharedPreferences(
				MainActivity.PREFERENCES,
				Context.MODE_PRIVATE,
			).getBoolean(MainActivity.KEY_PLAYBACK_HOT_SWAP, false)
			if (!PlaybackHotSwapPolicy.shouldReplace(enabled, activeQueue?.eventID, eventID)) return
			instance?.requestHotReplacement(eventID)
		}

		fun cancelPendingReplacement(context: Context) {
			instance?.replacementGeneration?.incrementAndGet()
			clearHotSwapState(context.applicationContext)
		}

		fun activeQueueSnapshot(): PlaybackQueue? = activeQueue

		fun stop(context: Context) {
			cancelPendingReplacement(context)
			activeQueue = null
			context.stopService(Intent(context, PlaybackService::class.java))
		}

		private fun mediaItems(context: Context, queue: PlaybackQueue): List<MediaItem> {
			val artworkUri = queue.artwork
				?.let { PlaybackMediaCache(context).cached(it) }
				?.let(Uri::fromFile)
			return queue.items.map { track ->
				MediaItem.Builder()
					.setMediaId(track.id)
					.setUri(
						Uri.Builder()
							.scheme(MEDIA_SCHEME)
							.authority(queue.eventID.toString())
							.appendPath(track.id)
							.build(),
					)
					.setMimeType(track.mime)
					.setMediaMetadata(
						MediaMetadata.Builder()
							.setTitle(track.title)
							.setArtist(track.artist.ifBlank { null })
							.setAlbumTitle(queue.name)
							.setArtworkUri(artworkUri)
							.build(),
					)
					.build()
			}
		}

		private fun updateHotSwapState(context: Context, eventID: Long, state: String) {
			context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
				.edit()
				.putLong(MainActivity.KEY_PLAYBACK_HOT_SWAP_EVENT_ID, eventID)
				.putString(MainActivity.KEY_PLAYBACK_HOT_SWAP_STATE, state)
				.apply()
		}

		private fun clearHotSwapState(context: Context) {
			context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
				.edit()
				.remove(MainActivity.KEY_PLAYBACK_HOT_SWAP_EVENT_ID)
				.remove(MainActivity.KEY_PLAYBACK_HOT_SWAP_STATE)
				.apply()
		}
	}
}
