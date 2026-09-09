package dev.migi.app

import android.content.Context
import android.os.ParcelFileDescriptor
import java.io.File
import org.json.JSONObject

/** Private voice API, independent from shared files and their notifications. */
internal class VoiceClient(context: Context) {
    private val prefs = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val endpoint = requireNotNull(prefs.getString(MainActivity.KEY_ENDPOINT, null)).trimEnd('/')
    private val pin = requireNotNull(prefs.getString(MainActivity.KEY_CERTIFICATE_PIN, null))
    private val credential = requireNotNull(CredentialStore(context).load())
    private fun parse(raw: String): JSONObject {
        check(!raw.startsWith("MIGI_ERROR:")) { raw.removePrefix("MIGI_ERROR:") }
        return JSONObject(raw)
    }
    fun upload(file: File): String {
        require(file.isFile && file.length() in 44..1_920_044)
        ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY).use { fd ->
            return parse(NativeQuicClient.uploadVoiceFile(endpoint, pin, credential,
                file.name, "audio/vnd.migi.voice-wav", fd.fd, file.length())).getString("id")
        }
    }
    fun state(id: String, decision: String = ""): JSONObject =
        parse(NativeQuicClient.voiceRequest(endpoint, pin, credential, id, decision))
}
