plugins {
    id("com.android.application")
}

val pilotVersionCode = providers.environmentVariable("MIGI_PILOT_VERSION_CODE")
    .orElse("1")
    .map(String::toInt)
val pilotVersionName = providers.environmentVariable("MIGI_PILOT_VERSION_NAME")
    .orElse(pilotVersionCode.map { "0.0.$it" })

val pilotKeystore = providers.environmentVariable("MIGI_PILOT_KEYSTORE")
val pilotKeyAlias = providers.environmentVariable("MIGI_PILOT_KEY_ALIAS")
val pilotStorePassword = providers.environmentVariable("MIGI_PILOT_STORE_PASSWORD")
val pilotKeyPassword = providers.environmentVariable("MIGI_PILOT_KEY_PASSWORD")
val hasReleaseSigning = listOf(
    pilotKeystore,
    pilotKeyAlias,
    pilotStorePassword,
    pilotKeyPassword,
).all { it.isPresent }

android {
    namespace = "dev.migi.pilot"
    compileSdk = 36

    buildFeatures {
        buildConfig = true
    }

    defaultConfig {
        applicationId = "dev.migi.pilot"
        minSdk = 34
        targetSdk = 36
        versionCode = pilotVersionCode.get()
        versionName = pilotVersionName.get()
    }

    if (hasReleaseSigning) {
        signingConfigs {
            create("pilotRelease") {
                storeFile = file(pilotKeystore.get())
                storePassword = pilotStorePassword.get()
                keyAlias = pilotKeyAlias.get()
                keyPassword = pilotKeyPassword.get()
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = false
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (hasReleaseSigning) {
                signingConfig = signingConfigs.getByName("pilotRelease")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
