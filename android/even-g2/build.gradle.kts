plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.migi.g2"
    compileSdk = 36
    ndkVersion = "27.2.12479018"
    externalNativeBuild { cmake { path = file("src/main/cpp/CMakeLists.txt"); version = "3.22.1" } }
    buildFeatures { buildConfig = true }
    defaultConfig {
        minSdk = 34
        // Match Migi's QUIC JNI ABI; do not advertise incomplete extra ABIs.
        ndk { abiFilters += "arm64-v8a" }
        consumerProguardFiles("consumer-rules.pro")
        buildConfigField("boolean", "DOCUMENT_PROBE", (System.getenv("MIGI_G2_DOCUMENT_PROBE") == "true").toString())
        buildConfigField("boolean", "WIDGET_PROBE", (System.getenv("MIGI_G2_WIDGET_PROBE") == "true").toString())
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation("ru.noties:jlatexmath-android:0.2.0")
}
