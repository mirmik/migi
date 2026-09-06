plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.migi.g2"
    compileSdk = 36
    buildFeatures { buildConfig = true }
    defaultConfig {
        minSdk = 34
        buildConfigField("boolean", "DOCUMENT_PROBE", (System.getenv("MIGI_G2_DOCUMENT_PROBE") == "true").toString())
        buildConfigField("boolean", "WIDGET_PROBE", (System.getenv("MIGI_G2_WIDGET_PROBE") == "true").toString())
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}
