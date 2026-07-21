plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.migi.app"
    compileSdk = 36

    defaultConfig {
        applicationId = "dev.migi.app"
        minSdk = 34
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
    }

    ndkVersion = "27.2.12479018"

    buildTypes {
        release {
            isMinifyEnabled = true
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
