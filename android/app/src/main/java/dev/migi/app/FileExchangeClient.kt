package dev.migi.app

import android.content.Context
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import java.io.File
import java.io.FileOutputStream
import java.time.Instant
import org.json.JSONArray
import org.json.JSONObject

data class SharedFile(
    val id: String,
    val name: String,
    val mime: String,
    val size: Long,
    val sha256: String,
    val source: String,
    val createdAt: Instant,
    val expiresAt: Instant,
)

internal class FileExchangeClient(private val context: Context) {
    private val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)

    fun list(): List<SharedFile> {
        val config = config()
        return parseList(
            checkResponse(
                NativeQuicClient.listSharedFiles(
                    config.endpoint,
                    config.pin,
                    config.credential,
                ),
            ),
        )
    }

    fun upload(uri: Uri): SharedFile {
        val config = config()
        val name = displayName(uri)
        val mime = context.contentResolver.getType(uri) ?: "application/octet-stream"
        val directory = File(context.cacheDir, "file-uploads").apply {
            check(mkdirs() || isDirectory) { "Failed to create upload cache" }
        }
        val temporary = File.createTempFile("upload-", ".tmp", directory)
        try {
            context.contentResolver.openInputStream(uri).use { input ->
                requireNotNull(input) { "Cannot open shared file" }
                FileOutputStream(temporary).use { output ->
                    val buffer = ByteArray(64 * 1024)
                    var total = 0L
                    while (true) {
                        val read = input.read(buffer)
                        if (read < 0) break
                        total += read
                        require(total <= MAX_FILE_BYTES) { "File exceeds ${MAX_FILE_BYTES / (1024 * 1024)} MiB" }
                        output.write(buffer, 0, read)
                    }
                    output.fd.sync()
                }
            }
            require(temporary.length() > 0) { "Cannot share an empty file" }
            ParcelFileDescriptor.open(temporary, ParcelFileDescriptor.MODE_READ_ONLY).use { descriptor ->
                return parseFile(
                    JSONObject(
                        checkResponse(
                            NativeQuicClient.uploadSharedFile(
                                config.endpoint,
                                config.pin,
                                config.credential,
                                name,
                                mime,
                                descriptor.fd,
                                temporary.length(),
                            ),
                        ),
                    ),
                )
            }
        } finally {
            temporary.delete()
        }
    }

    fun download(file: SharedFile, destination: Uri) {
        val temporary = downloadVerified(
            file = file,
            directoryName = "file-downloads",
            prefix = "download-",
            suffix = ".tmp",
            maxBytes = MAX_FILE_BYTES,
        )
        try {
            temporary.inputStream().use { input ->
                context.contentResolver.openOutputStream(destination, "wt").use { output ->
                    requireNotNull(output) { "Cannot open download destination" }
                    input.copyTo(output, 64 * 1024)
                    output.flush()
                }
            }
        } finally {
            temporary.delete()
        }
    }

    fun downloadForViewing(file: SharedFile): File {
        require(file.isViewableHTML()) { "Only HTML files can be opened in Migi" }
        File(context.cacheDir, HtmlViewerPolicy.CACHE_DIRECTORY)
            .listFiles()
            ?.filter {
                it.name.startsWith(HtmlViewerPolicy.FILE_PREFIX) &&
                    it.name.endsWith(".html")
            }
            ?.forEach(File::delete)
        return downloadVerified(
            file = file,
            directoryName = HtmlViewerPolicy.CACHE_DIRECTORY,
            prefix = HtmlViewerPolicy.FILE_PREFIX,
            suffix = ".html",
            maxBytes = HtmlViewerPolicy.MAX_HTML_BYTES,
        )
    }

    private fun downloadVerified(
        file: SharedFile,
        directoryName: String,
        prefix: String,
        suffix: String,
        maxBytes: Long,
    ): File {
        require(file.size in 1..maxBytes) {
            "File exceeds ${maxBytes / (1024 * 1024)} MiB viewer limit"
        }
        val config = config()
        val directory = File(context.cacheDir, directoryName).apply {
            check(mkdirs() || isDirectory) { "Failed to create download cache" }
        }
        val temporary = File.createTempFile(prefix, suffix, directory)
        return try {
            ParcelFileDescriptor.open(
                temporary,
                ParcelFileDescriptor.MODE_READ_WRITE or ParcelFileDescriptor.MODE_TRUNCATE,
            ).use { descriptor ->
                val verified = JSONObject(checkResponse(
                    NativeQuicClient.downloadSharedFile(
                        config.endpoint,
                        config.pin,
                        config.credential,
                        file.id,
                        descriptor.fd,
                        maxBytes,
                    ),
                ))
                require(verified.getLong("bytes") == file.size) { "Downloaded size differs from metadata" }
                require(verified.getString("sha256").equals(file.sha256, ignoreCase = true)) {
                    "Downloaded digest differs from metadata"
                }
            }
            require(temporary.length() == file.size) { "Downloaded size differs from metadata" }
            temporary
        } catch (error: Throwable) {
            temporary.delete()
            throw error
        }
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

    private fun displayName(uri: Uri): String {
        val queried = context.contentResolver.query(
            uri,
            arrayOf(OpenableColumns.DISPLAY_NAME),
            null,
            null,
            null,
        )?.use { cursor ->
            if (cursor.moveToFirst()) cursor.getString(0) else null
        }
        return (queried ?: uri.lastPathSegment ?: "shared-file")
            .substringAfterLast('/')
            .substringAfterLast('\\')
            .trim()
            .take(255)
            .takeIf { it.isNotEmpty() }
            ?: "shared-file"
    }

    private data class Config(val endpoint: String, val pin: String, val credential: String)

    companion object {
        const val MAX_FILE_BYTES = 100L * 1024 * 1024

        fun checkResponse(response: String): String {
            check(!response.startsWith("MIGI_ERROR:")) {
                response.removePrefix("MIGI_ERROR:")
            }
            return response
        }

        private fun parseList(response: String): List<SharedFile> {
            val array = JSONArray(response)
            return buildList {
                for (index in 0 until array.length()) {
                    add(parseFile(array.getJSONObject(index)))
                }
            }
        }

        private fun parseFile(json: JSONObject) = SharedFile(
            id = json.getString("id"),
            name = json.getString("name"),
            mime = json.getString("mime"),
            size = json.getLong("size"),
            sha256 = json.getString("sha256"),
            source = json.getString("source"),
            createdAt = Instant.parse(json.getString("created_at")),
            expiresAt = Instant.parse(json.getString("expires_at")),
        )
    }
}

internal fun SharedFile.isViewableHTML(): Boolean {
    val normalizedMime = mime.substringBefore(';').trim()
    return normalizedMime.equals("text/html", ignoreCase = true) ||
        name.endsWith(".html", ignoreCase = true) ||
        name.endsWith(".htm", ignoreCase = true)
}
