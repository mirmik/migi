package dev.migi.app

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService

class PlaybackService : MediaSessionService() {
	private lateinit var player: ExoPlayer
	private lateinit var mediaSession: MediaSession

	override fun onCreate() {
		super.onCreate()
		player = ExoPlayer.Builder(this).build().apply {
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

	override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
		val result = super.onStartCommand(intent, flags, startId)
		when (intent?.action) {
			ACTION_PLAY_QUEUE -> playQueue(intent.getLongExtra(EXTRA_EVENT_ID, 0))
		}
		return result
	}

	private fun playQueue(eventID: Long) {
		runCatching {
			val queue = requireNotNull(PlaybackQueueRepository(this).current()) {
				"No playback queue is available"
			}
			require(queue.eventID == eventID) { "Playback queue changed before it could start" }
			val files = PlaybackMediaCache(this).prepared(queue)
			val items = queue.items.zip(files).map { (track, file) ->
				MediaItem.Builder()
					.setMediaId(track.id)
					.setUri(Uri.fromFile(file))
					.setMimeType(track.mime)
					.setMediaMetadata(
						MediaMetadata.Builder()
							.setTitle(track.title)
							.setArtist(track.artist.ifBlank { null })
							.setAlbumTitle(queue.name)
							.build(),
					)
					.build()
			}
			player.setMediaItems(items)
			player.prepare()
			player.play()
			Log.i(TAG, "Started queue $eventID with ${items.size} tracks")
		}.onFailure { error ->
			Log.e(TAG, "Failed to start playback queue $eventID", error)
			stopSelf()
		}
	}

	override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession = mediaSession

	override fun onDestroy() {
		mediaSession.release()
		player.release()
		super.onDestroy()
	}

	companion object {
		private const val ACTION_PLAY_QUEUE = "dev.migi.app.action.PLAY_QUEUE"
		private const val EXTRA_EVENT_ID = "event_id"
		private const val PLAYBACK_ACTIVITY_REQUEST = 41
		private const val TAG = "MigiPlayback"

		fun start(context: Context, eventID: Long) {
			context.startForegroundService(
				Intent(context, PlaybackService::class.java)
					.setAction(ACTION_PLAY_QUEUE)
					.putExtra(EXTRA_EVENT_ID, eventID),
			)
		}

		fun stop(context: Context) {
			context.stopService(Intent(context, PlaybackService::class.java))
		}
	}
}
