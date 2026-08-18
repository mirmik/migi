package dev.migi.app

import android.content.Context
import android.os.ParcelFileDescriptor
import java.io.File
import java.security.MessageDigest
import java.nio.file.AtomicMoveNotSupportedException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import org.json.JSONObject

internal class PlaybackMediaCache(private val context: Context) {
	private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
	private val directory = File(context.filesDir, CACHE_DIRECTORY).apply {
		check(mkdirs() || isDirectory) { "Failed to create playback media cache" }
	}

	fun clear() {
		directory.listFiles()?.forEach(File::delete)
	}

	/** Materializes and verifies one track when Media3 first asks for it. */
	@Synchronized
	fun prepare(track: PlaybackTrack): File {
		val destination = destination(track)
		if (destination.isFile && destination.length() == track.size && sha256(destination) == track.sha256) {
			destination.setLastModified(System.currentTimeMillis())
			return destination
		}
		if (destination.exists() && !destination.delete()) {
			error("Cannot replace invalid cached track")
		}
		val config = config()
		val temporary = File.createTempFile("media-", ".tmp", directory)
		try {
			ParcelFileDescriptor.open(
				temporary,
				ParcelFileDescriptor.MODE_READ_WRITE or ParcelFileDescriptor.MODE_TRUNCATE,
			).use { descriptor ->
				val response = NativeQuicClient.downloadMedia(
					config.endpoint,
					config.pin,
					config.credential,
					track.id,
					descriptor.fd,
					track.size,
				)
				check(!response.startsWith("MIGI_ERROR:")) { response.removePrefix("MIGI_ERROR:") }
				val verified = JSONObject(response)
				require(verified.getLong("bytes") == track.size) { "Downloaded track size differs from queue" }
				require(verified.getString("sha256").equals(track.sha256, ignoreCase = true)) {
					"Downloaded track digest differs from queue"
				}
			}
			require(temporary.length() == track.size) { "Downloaded track size differs from queue" }
			try {
				Files.move(
					temporary.toPath(),
					destination.toPath(),
					StandardCopyOption.ATOMIC_MOVE,
					StandardCopyOption.REPLACE_EXISTING,
				)
			} catch (_: AtomicMoveNotSupportedException) {
				Files.move(
					temporary.toPath(),
					destination.toPath(),
					StandardCopyOption.REPLACE_EXISTING,
				)
			}
			destination.setLastModified(System.currentTimeMillis())
			trim(destination)
			return destination
		} finally {
			temporary.delete()
		}
	}

	private fun destination(track: PlaybackTrack): File =
		File(directory, cacheKey(track))

	private fun trim(protected: File) {
		val files = directory.listFiles()
			?.filter { it.isFile && !it.name.endsWith(".tmp") }
			.orEmpty()
		var total = files.sumOf(File::length)
		for (file in files.filter { it != protected }.sortedBy(File::lastModified)) {
			if (total <= MAX_CACHE_BYTES) break
			val bytes = file.length()
			if (file.delete()) total -= bytes
		}
	}

	private fun cacheKey(track: PlaybackTrack): String = track.sha256 + extensionFor(track.mime)

	private fun config(): Config {
		val endpoint = preferences.getString(MainActivity.KEY_ENDPOINT, null)
			?.trimEnd('/')
			?.takeIf { it.startsWith("https://") }
			?: error("Migi server is not configured")
		val pin = preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, null)
			?.takeIf { it.isNotBlank() }
			?: error("Server certificate pin is not configured")
		val credential = CredentialStore(context).load() ?: error("Device is not paired")
		return Config(endpoint, pin, credential)
	}

	private fun sha256(file: File): String {
		val digest = MessageDigest.getInstance("SHA-256")
		file.inputStream().use { input ->
			val buffer = ByteArray(64 * 1024)
			while (true) {
				val read = input.read(buffer)
				if (read < 0) break
				digest.update(buffer, 0, read)
			}
		}
		return digest.digest().joinToString("") { byte ->
			(byte.toInt() and 0xff).toString(16).padStart(2, '0')
		}
	}

	private fun extensionFor(mime: String): String = when (mime.lowercase()) {
		"audio/mpeg", "audio/mp3" -> ".mp3"
		"audio/ogg", "audio/opus" -> ".ogg"
		"audio/flac" -> ".flac"
		"audio/mp4", "audio/aac", "audio/x-m4a" -> ".m4a"
		"audio/wav", "audio/x-wav" -> ".wav"
		else -> ".audio"
	}

	private data class Config(val endpoint: String, val pin: String, val credential: String)

	companion object {
		private const val CACHE_DIRECTORY = "playback-media"
		private const val MAX_CACHE_BYTES = 512L * 1024 * 1024
	}
}
