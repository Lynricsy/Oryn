import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.lynricsy.oryn.mobile"
    compileSdk = 37

    defaultConfig {
        minSdk = 33
        consumerProguardFiles("consumer-rules.pro")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

kotlin {
    compilerOptions {
        jvmTarget = JvmTarget.JVM_17
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.webkit:webkit:1.14.0")
    implementation("org.unifiedpush.android:connector:3.3.5")
    implementation(project(":tauri-android"))
    testImplementation("junit:junit:4.13.2")
    // android.jar 里的 org.json 是桩实现，JVM 单测要真实现
    testImplementation("org.json:json:20250517")
}
