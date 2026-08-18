package dev.migi.app

import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
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

class PlaybackService : MediaSessionService() {
	private lateinit var player: ExoPlayer
	private lateinit var mediaSession: MediaSession

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
	}

	override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession = mediaSession

	override fun onDestroy() {
		mediaSession.release()
		player.release()
		activeQueue = null
		super.onDestroy()
	}

	companion object {
		private const val PLAYBACK_ACTIVITY_REQUEST = 41
		private const val TAG = "MigiPlayback"
		private const val MEDIA_SCHEME = "migi-media"
		@Volatile private var activeQueue: PlaybackQueue? = null

		fun start(context: Context, eventID: Long, onComplete: (Throwable?) -> Unit) {
			val application = context.applicationContext
			val token = SessionToken(application, ComponentName(application, PlaybackService::class.java))
			val controllerFuture = MediaController.Builder(application, token).buildAsync()
			controllerFuture.addListener({
				val result = runCatching {
					val queue = requireNotNull(PlaybackQueueRepository(application).current()) {
						"No playback queue is available"
					}
					require(queue.eventID == eventID) { "Playback queue changed before it could start" }
					activeQueue = queue
					val artworkUri = queue.artwork
						?.let { PlaybackMediaCache(application).cached(it) }
						?.let(Uri::fromFile)
					val items = queue.items.map { track ->
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
					val controller = controllerFuture.get()
					controller.setMediaItems(items)
					controller.prepare()
					controller.play()
					Log.i(TAG, "Started queue $eventID with ${items.size} tracks through MediaController")
				}
				MediaController.releaseFuture(controllerFuture)
				val error = result.exceptionOrNull()?.let { it.cause ?: it }
				if (error != null) Log.e(TAG, "Failed to start playback queue $eventID", error)
				onComplete(error)
			}, context.mainExecutor)
		}

		fun stop(context: Context) {
			context.stopService(Intent(context, PlaybackService::class.java))
		}
	}
}
