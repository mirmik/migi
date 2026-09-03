package dev.migi.app

import android.content.Context
import java.time.Instant
import org.json.JSONArray
import org.json.JSONObject

data class SavedPlaylistSummary(
    val id: String,
    val name: String,
    val trackCount: Int,
    val updatedAt: Instant,
)

internal class SavedPlaylistClient(private val context: Context) {
    private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)

    fun list(): List<SavedPlaylistSummary> {
        val config = config()
        return parseList(checkResponse(
            NativeQuicClient.listSavedPlaylists(
                config.endpoint,
                config.pin,
                config.credential,
            ),
        ))
    }

    fun start(playlistID: String): Long {
        require(MEDIA_ID.matches(playlistID)) { "Saved playlist ID is malformed" }
        val config = config()
        return parseStartedEvent(checkResponse(
            NativeQuicClient.startSavedPlaylist(
                config.endpoint,
                config.pin,
                config.credential,
                playlistID,
            ),
        ))
    }

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

    private data class Config(val endpoint: String, val pin: String, val credential: String)

    companion object {
        private val MEDIA_ID = Regex("^[a-f0-9]{32}$")

        internal fun checkResponse(response: String): String {
            check(!response.startsWith("MIGI_ERROR:")) {
                response.removePrefix("MIGI_ERROR:")
            }
            return response
        }

        internal fun parseList(response: String): List<SavedPlaylistSummary> {
            val array = JSONArray(response)
            return buildList(array.length()) {
                for (index in 0 until array.length()) {
                    val item = array.getJSONObject(index)
                    val id = item.getString("id")
                    val name = item.getString("name")
                    val trackCount = item.getInt("track_count")
                    add(validate(SavedPlaylistSummary(
                        id = id,
                        name = name,
                        trackCount = trackCount,
                        updatedAt = Instant.parse(item.getString("updated_at")),
                    )))
                }
            }
        }

        internal fun validate(playlist: SavedPlaylistSummary): SavedPlaylistSummary {
            require(MEDIA_ID.matches(playlist.id)) { "Saved playlist ID is malformed" }
            require(
                playlist.name.isNotBlank() &&
                    playlist.name == playlist.name.trim() &&
                    playlist.name.codePointCount(0, playlist.name.length) <= 128,
            ) { "Saved playlist name is malformed" }
            require(playlist.trackCount > 0) { "Saved playlist track count is invalid" }
            return playlist
        }

        internal fun parseStartedEvent(response: String): Long =
            JSONObject(response).getLong("id").also {
                require(it > 0) { "Saved playlist queue response has no valid event ID" }
            }
    }
}
