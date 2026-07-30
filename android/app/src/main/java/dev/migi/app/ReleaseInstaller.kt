package dev.migi.app

import android.app.Activity
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.os.ParcelFileDescriptor
import android.provider.Settings
import java.io.File
import java.io.FileInputStream
import java.security.MessageDigest
import kotlin.concurrent.thread
import org.json.JSONObject

class ReleaseInstaller(private val context: Context) {
    private val repository = ReleaseRepository(context)

    fun download(artifactID: String, onComplete: (Result<Unit>) -> Unit) {
        thread(name = "migi-release-download") {
            val result = runCatching {
                val release = requireNotNull(repository.findRelease(artifactID)) {
                    "Unknown release"
                }
                val connection = connection()
                repository.updateState(artifactID, ReleaseRepository.STATE_DOWNLOADING)
                val metadataResponse = NativeQuicClient.releaseMetadata(
                    connection.endpoint,
                    connection.pin,
                    connection.credential,
                    artifactID,
                )
                checkNativeResponse(metadataResponse)
                val metadata = parseMetadata(metadataResponse)
                check(metadata.artifactID == artifactID)
                check(metadata.packageName == release.artifact.packageName)
                check(metadata.versionCode == release.artifact.versionCode)
                check(metadata.versionName == release.artifact.versionName)
                check(metadata.size in 1..MAX_APK_BYTES)
                repository.updateMetadata(artifactID, metadata)

                val directory = File(context.cacheDir, "releases").apply {
                    check(mkdirs() || isDirectory) { "Failed to create release cache" }
                }
                val file = File(directory, "$artifactID.apk")
                ParcelFileDescriptor.open(
                    file,
                    ParcelFileDescriptor.MODE_CREATE or
                        ParcelFileDescriptor.MODE_TRUNCATE or
                        ParcelFileDescriptor.MODE_READ_WRITE,
                ).use { descriptor ->
                    val downloadResponse = NativeQuicClient.downloadRelease(
                        connection.endpoint,
                        connection.pin,
                        connection.credential,
                        artifactID,
                        descriptor.fd,
                        metadata.size,
                    )
                    checkNativeResponse(downloadResponse)
                    val download = JSONObject(downloadResponse)
                    check(download.getLong("bytes") == metadata.size) { "Downloaded size differs" }
                    check(download.getString("sha256").equals(metadata.sha256, ignoreCase = true)) {
                        "Downloaded digest differs"
                    }
                }
                verifyAPK(file, metadata)
                repository.updateState(
                    artifactID,
                    ReleaseRepository.STATE_DOWNLOADED,
                    tempPath = file.absolutePath,
                )
            }.onFailure { error ->
                repository.findRelease(artifactID)?.let {
                    repository.updateState(
                        artifactID,
                        ReleaseRepository.STATE_FAILED,
                        tempPath = it.tempPath,
                        error = error.message ?: error.javaClass.simpleName,
                    )
                }
            }
            (context as? Activity)?.runOnUiThread { onComplete(result) } ?: onComplete(result)
        }
    }

    fun install(activity: Activity, artifactID: String, onComplete: (Result<Unit>) -> Unit) {
        val result = runCatching {
            check(context.packageManager.canRequestPackageInstalls()) {
                "Allow Migi to install apps from this source first"
            }
            val release = requireNotNull(repository.findRelease(artifactID))
            val path = requireNotNull(release.tempPath) { "Download the APK first" }
            val metadata = release.toMetadata()
            val file = File(path)
            verifyAPK(file, metadata)

            val installer = context.packageManager.packageInstaller
            val resumableSession = release.sessionID?.takeIf { expected ->
                installer.mySessions.any { it.sessionId == expected }
            }
            val sessionID = resumableSession ?: installer.createSession(
                PackageInstaller.SessionParams(
                    PackageInstaller.SessionParams.MODE_FULL_INSTALL,
                ).apply {
                    setAppPackageName(metadata.packageName)
                    setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_REQUIRED)
                },
            )
            installer.openSession(sessionID).use { session ->
                if (resumableSession == null) {
                    FileInputStream(file).use { input ->
                        session.openWrite("base.apk", 0, file.length()).use { output ->
                            input.copyTo(output)
                            session.fsync(output)
                        }
                    }
                }
                repository.updateState(
                    artifactID,
                    ReleaseRepository.STATE_INSTALLING,
                    tempPath = path,
                    sessionID = sessionID,
                )
                foregroundActivity = activity
                session.commit(statusReceiver(artifactID, sessionID).intentSender)
            }
        }
        onComplete(result)
    }

    fun reconcileSessions() {
        val installer = context.packageManager.packageInstaller
        val owned = installer.mySessions.associateBy { it.sessionId }
        val releases = repository.listReleases()
        val referenced = releases.mapNotNull { it.sessionID }.toSet()
        for (session in owned.values) {
            if (session.sessionId !in referenced) {
                runCatching { installer.abandonSession(session.sessionId) }
            }
        }
        for (release in releases.filter { it.state == ReleaseRepository.STATE_INSTALLING }) {
            val sessionID = release.sessionID
            if (sessionID == null || sessionID !in owned) {
                val installed = runCatching {
                    context.packageManager.getPackageInfo(release.artifact.packageName, 0)
                }.getOrNull()
                if (installed != null && installed.longVersionCode >= release.artifact.versionCode) {
                    release.tempPath?.let { File(it).delete() }
                    repository.updateState(release.artifact.id, ReleaseRepository.STATE_INSTALLED)
                } else {
                    repository.updateState(
                        release.artifact.id,
                        ReleaseRepository.STATE_FAILED,
                        tempPath = release.tempPath,
                        error = "Installer session disappeared; tap Install to retry",
                    )
                }
            }
        }
    }

    fun unknownSourcesSettingsIntent(): Intent = Intent(
        Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
        android.net.Uri.parse("package:${context.packageName}"),
    )

    private fun verifyAPK(file: File, metadata: ReleaseMetadata) {
        check(file.isFile && file.length() == metadata.size) { "APK size differs" }
        check(file.sha256().equals(metadata.sha256, ignoreCase = true)) { "APK digest differs" }
        val pilotPin = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
            .getString(MainActivity.KEY_PILOT_SIGNER_SHA256, null)
        val localPin = resolveReleaseSignerPin(
            packageName = metadata.packageName,
            pilotPin = pilotPin,
            selfPackageName = BuildConfig.APPLICATION_ID,
            selfPin = BuildConfig.SELF_UPDATE_SIGNER_SHA256,
        )
        check(localPin == metadata.signerSHA256.lowercase()) { "Release signer is not locally pinned" }

        val packageInfo = requireNotNull(
            context.packageManager.getPackageArchiveInfo(
                file.absolutePath,
                PackageManager.PackageInfoFlags.of(PackageManager.GET_SIGNING_CERTIFICATES.toLong()),
            ),
        ) { "Android could not parse APK" }
        check(packageInfo.packageName == metadata.packageName) { "APK package differs" }
        check(packageInfo.longVersionCode == metadata.versionCode) { "APK version differs" }
        val signing = requireNotNull(packageInfo.signingInfo) { "APK has no signing information" }
        check(!signing.hasMultipleSigners()) { "Multiple APK signers are not supported" }
        val history = requireNotNull(signing.signingCertificateHistory)
        check(history.size == 1) { "Signing certificate rotation is not supported" }
        check(history.single().toByteArray().sha256() == localPin) { "APK signer differs" }

        val installed = runCatching {
            context.packageManager.getPackageInfo(
                metadata.packageName,
                PackageManager.PackageInfoFlags.of(PackageManager.GET_SIGNING_CERTIFICATES.toLong()),
            )
        }.getOrNull()
        if (installed != null) {
            check(metadata.versionCode > installed.longVersionCode) { "Same-version or downgrade install rejected" }
            check(
                context.packageManager.hasSigningCertificate(
                    metadata.packageName,
                    localPin.hexBytes(),
                    PackageManager.CERT_INPUT_SHA256,
                ),
            ) { "Installed package has a different signer" }
        }
    }

    private fun parseMetadata(raw: String): ReleaseMetadata {
        val json = JSONObject(raw)
        return ReleaseMetadata(
            artifactID = json.getString("artifact_id"),
            packageName = json.getString("package_name"),
            versionCode = json.getLong("version_code"),
            versionName = json.getString("version_name"),
            size = json.getLong("size"),
            sha256 = json.getString("sha256").lowercase(),
            signerSHA256 = json.getString("signer_sha256").lowercase(),
            publisher = json.getString("publisher"),
            releaseNotes = json.optString("release_notes"),
            sourceRevision = json.optString("source_revision"),
            buildID = json.optString("build_id"),
        )
    }

    private fun PendingRelease.toMetadata(): ReleaseMetadata = ReleaseMetadata(
        artifactID = artifact.id,
        packageName = artifact.packageName,
        versionCode = artifact.versionCode,
        versionName = artifact.versionName,
        size = requireNotNull(size),
        sha256 = requireNotNull(sha256),
        signerSHA256 = requireNotNull(signerSHA256),
        publisher = publisher.orEmpty(),
        releaseNotes = releaseNotes.orEmpty(),
        sourceRevision = "",
        buildID = "",
    )

    private fun connection(): Connection {
        val preferences = context.getSharedPreferences(MainActivity.PREFERENCES, Context.MODE_PRIVATE)
        return Connection(
            endpoint = requireNotNull(preferences.getString(MainActivity.KEY_ENDPOINT, null)),
            pin = requireNotNull(preferences.getString(MainActivity.KEY_CERTIFICATE_PIN, null)),
            credential = requireNotNull(CredentialStore(context).load()),
        )
    }

    private fun checkNativeResponse(value: String) {
        check(!value.startsWith("MIGI_ERROR:")) { value.removePrefix("MIGI_ERROR:") }
    }

    private fun statusReceiver(artifactID: String, sessionID: Int): PendingIntent {
        val callback = Intent(context, InstallResultReceiver::class.java).apply {
            action = "$ACTION_INSTALL_RESULT.$sessionID"
            setPackage(context.packageName)
            putExtra(EXTRA_ARTIFACT_ID, artifactID)
            putExtra(EXTRA_EXPECTED_SESSION_ID, sessionID)
        }
        return PendingIntent.getBroadcast(
            context,
            sessionID,
            callback,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
        )
    }

    private data class Connection(val endpoint: String, val pin: String, val credential: String)

    companion object {
        private const val MAX_APK_BYTES = 256L shl 20
        const val ACTION_INSTALL_RESULT = "dev.migi.app.action.INSTALL_RESULT"
        const val EXTRA_ARTIFACT_ID = "artifact_id"
        const val EXTRA_EXPECTED_SESSION_ID = "expected_session_id"

        @Volatile
        var foregroundActivity: Activity? = null
    }
}

internal fun resolveReleaseSignerPin(
    packageName: String,
    pilotPin: String?,
    selfPackageName: String,
    selfPin: String,
): String {
    val (candidate, missingMessage) = when (packageName) {
        PILOT_PACKAGE -> pilotPin to "Configure the pilot signer SHA-256 in Migi"
        selfPackageName -> selfPin to "Migi self-update signer pin is not configured"
        else -> error("Package is not locally allowlisted")
    }
    check(!candidate.isNullOrBlank()) { missingMessage }
    val normalized = candidate
        .filterNot { it == ':' || it.isWhitespace() }
        .lowercase()
    check(
        candidate.all {
            it.isDigit() || it.lowercaseChar() in 'a'..'f' || it == ':' || it.isWhitespace()
        } && normalized.matches(Regex("[0-9a-f]{64}")),
    ) {
        if (packageName == PILOT_PACKAGE) {
            "Configure the pilot signer SHA-256 in Migi"
        } else {
            "Migi self-update signer pin is malformed"
        }
    }
    return normalized
}

private const val PILOT_PACKAGE = "dev.migi.pilot"

class InstallResultReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val artifactID = intent.getStringExtra(ReleaseInstaller.EXTRA_ARTIFACT_ID) ?: return
        val expectedSession = intent.getIntExtra(ReleaseInstaller.EXTRA_EXPECTED_SESSION_ID, -1)
        val actualSession = intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1)
        val repository = ReleaseRepository(context)
        val release = repository.findRelease(artifactID) ?: return
        if (expectedSession < 0 || actualSession != expectedSession || release.sessionID != expectedSession) {
            return
        }
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirmation = intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                val activity = ReleaseInstaller.foregroundActivity
                if (confirmation != null && activity != null) {
                    activity.startActivity(confirmation)
                } else {
                    repository.updateState(
                        artifactID,
                        ReleaseRepository.STATE_FAILED,
                        tempPath = release.tempPath,
                        sessionID = expectedSession,
                        error = "Open Migi and tap Install again to confirm",
                    )
                }
            }
            PackageInstaller.STATUS_SUCCESS -> {
                release.tempPath?.let { File(it).delete() }
                repository.updateState(artifactID, ReleaseRepository.STATE_INSTALLED)
            }
            else -> repository.updateState(
                artifactID,
                ReleaseRepository.STATE_FAILED,
                tempPath = release.tempPath,
                sessionID = expectedSession,
                error = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)
                    ?: "Android rejected installation",
            )
        }
    }
}

private fun File.sha256(): String = FileInputStream(this).use { input ->
    val digest = MessageDigest.getInstance("SHA-256")
    val buffer = ByteArray(64 * 1024)
    while (true) {
        val read = input.read(buffer)
        if (read < 0) break
        digest.update(buffer, 0, read)
    }
    digest.digest().joinToString("") { "%02x".format(it) }
}

private fun ByteArray.sha256(): String =
    MessageDigest.getInstance("SHA-256").digest(this).joinToString("") { "%02x".format(it) }

private fun String.hexBytes(): ByteArray =
    chunked(2).map { it.toInt(16).toByte() }.toByteArray()
