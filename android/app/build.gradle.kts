plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val releaseEnvironment = listOf(
    "MIGI_VERSION_CODE",
    "MIGI_VERSION_NAME",
    "MIGI_RELEASE_SIGNER_SHA256",
    "MIGI_KEYSTORE",
    "MIGI_KEY_ALIAS",
    "MIGI_STORE_PASSWORD",
    "MIGI_KEY_PASSWORD",
)

fun normalizedSignerPin(value: String): String {
    val normalized = value
        .filterNot { it == ':' || it.isWhitespace() }
        .lowercase()
    require(
        value.all { it.isDigit() || it.lowercaseChar() in 'a'..'f' || it == ':' || it.isWhitespace() } &&
            normalized.matches(Regex("[0-9a-f]{64}")),
    ) {
        "MIGI_RELEASE_SIGNER_SHA256 must be a SHA-256 certificate digest"
    }
    return normalized
}

fun positiveVersionCode(value: String, source: String): Int =
    requireNotNull(value.toIntOrNull()?.takeIf { it in 1..2_100_000_000 }) {
        "$source must be an integer from 1 through 2100000000"
    }

fun validVersionName(value: String, source: String): String {
    require(value.isNotBlank() && value == value.trim() && value.length <= 64) {
        "$source must contain 1-64 non-surrounding-whitespace characters"
    }
    require(value.none { it.isISOControl() }) {
        "$source must not contain control characters"
    }
    return value
}

val releaseVersionCode = providers.environmentVariable("MIGI_VERSION_CODE")
val releaseVersionName = providers.environmentVariable("MIGI_VERSION_NAME")
val releaseKeystore = providers.environmentVariable("MIGI_KEYSTORE")
val releaseKeyAlias = providers.environmentVariable("MIGI_KEY_ALIAS")
val releaseStorePassword = providers.environmentVariable("MIGI_STORE_PASSWORD")
val releaseKeyPassword = providers.environmentVariable("MIGI_KEY_PASSWORD")
val hasReleaseSigning = listOf(
    releaseKeystore,
    releaseKeyAlias,
    releaseStorePassword,
    releaseKeyPassword,
).all { it.isPresent }

val configuredVersionCode = releaseVersionCode.orNull?.let {
    positiveVersionCode(it, "MIGI_VERSION_CODE")
} ?: 1
val configuredVersionName = releaseVersionName.orNull?.let {
    validVersionName(it, "MIGI_VERSION_NAME")
} ?: "0.1.0"

android {
    namespace = "dev.migi.app"
    compileSdk = 36

    buildFeatures {
        buildConfig = true
    }

    defaultConfig {
        applicationId = "dev.migi.app"
        minSdk = 34
        targetSdk = 36
        versionCode = configuredVersionCode
        versionName = configuredVersionName
    }

    ndkVersion = "27.2.12479018"

    if (hasReleaseSigning) {
        signingConfigs {
            create("migiRelease") {
                storeFile = file(releaseKeystore.get())
                storePassword = releaseStorePassword.get()
                keyAlias = releaseKeyAlias.get()
                keyPassword = releaseKeyPassword.get()
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = false
                enableV4Signing = false
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            if (hasReleaseSigning) {
                signingConfig = signingConfigs.getByName("migiRelease")
            }
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    sourceSets.getByName("main").jniLibs.srcDir(layout.buildDirectory.dir("generated/jniLibs"))
}

dependencies {
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.media3:media3-exoplayer:1.10.1")
    implementation("androidx.media3:media3-session:1.10.1")
    testImplementation("junit:junit:4.13.2")
}

val validateMigiReleaseConfiguration by tasks.registering {
    group = "verification"
    description = "Fail closed unless every Migi release identity and signing input is valid"
    doLast {
        val values = releaseEnvironment.associateWith {
            providers.environmentVariable(it).orNull
        }
        val missing = values.filterValues { it.isNullOrBlank() }.keys
        check(missing.isEmpty()) {
            "Missing Migi release environment: ${missing.sorted().joinToString()}"
        }
        positiveVersionCode(requireNotNull(values["MIGI_VERSION_CODE"]), "MIGI_VERSION_CODE")
        validVersionName(requireNotNull(values["MIGI_VERSION_NAME"]), "MIGI_VERSION_NAME")
        normalizedSignerPin(requireNotNull(values["MIGI_RELEASE_SIGNER_SHA256"]))

        val keystore = file(requireNotNull(values["MIGI_KEYSTORE"])).canonicalFile
        val repository = rootProject.projectDir.parentFile.canonicalFile
        check(keystore.isAbsolute && keystore.isFile) {
            "MIGI_KEYSTORE must name an existing absolute regular file"
        }
        check(
            keystore != repository &&
                !keystore.path.startsWith(repository.path + File.separator),
        ) {
            "MIGI_KEYSTORE must stay outside the repository"
        }
    }
}

tasks.configureEach {
    if (name == "preReleaseBuild") {
        dependsOn(validateMigiReleaseConfiguration)
    }
}

val buildNativeQuiche by tasks.registering(Exec::class) {
    val outputDirectory = layout.buildDirectory.dir("generated/jniLibs")
    val nativeDirectory = rootProject.file("native")
    inputs.files(fileTree(nativeDirectory.resolve("src")), nativeDirectory.resolve("Cargo.toml"), nativeDirectory.resolve("Cargo.lock"))
    outputs.dir(outputDirectory)
    environment("ANDROID_NDK_HOME", android.ndkDirectory.absolutePath)
    workingDir(nativeDirectory)
    commandLine(
        "cargo", "ndk",
        "-t", "arm64-v8a",
        "-P", "34",
        "-o", outputDirectory.get().asFile.absolutePath,
        "build", "--offline", "--release",
    )
}

tasks.named("preBuild").configure {
    dependsOn(buildNativeQuiche)
}
