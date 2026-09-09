package dev.migi.app

import android.content.Context
import org.json.JSONObject

internal class AgentChatClient(context: Context) {
    private val prefs = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
    private val endpoint = requireNotNull(prefs.getString(MainActivity.KEY_ENDPOINT, null)).trimEnd('/')
    private val pin = requireNotNull(prefs.getString(MainActivity.KEY_CERTIFICATE_PIN, null))
    private val credential = requireNotNull(CredentialStore(context).load())
    fun request(body: String = "", limit: Int = 40): JSONObject {
        val raw = NativeQuicClient.chatRequest(endpoint, pin, credential, body, limit)
        if (raw.startsWith("MIGI_ERROR:")) throw ChatError(raw.removePrefix("MIGI_ERROR:"))
        return JSONObject(raw)
    }
    class ChatError(message: String) : Exception(message) {
        val rejected = Regex("\\b4\\d\\d\\b").containsMatchIn(message)
    }
}
