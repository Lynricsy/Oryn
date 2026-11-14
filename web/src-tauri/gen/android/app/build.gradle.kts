import java.io.FileInputStream
import java.util.Properties
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("rust")
}

val tauriProperties = Properties().apply {
    val propFile = file("tauri.properties")
    if (propFile.exists()) {
        propFile.inputStream().use { load(it) }
    }
}

// 发行渠道：foss（只有 UnifiedPush，无任何 Google 依赖）/ gms（另带 FCM）。
// 每次构建只启用一个，由 Gradle 属性 orynDistribution 选择（缺省 foss；CI 用 ORG_GRADLE_PROJECT_orynDistribution）。
// tauri CLI 只认 assembleUniversal<构建类型> 与 apk/universal/… 路径，见文末的产物复制。
val distributions = listOf("foss", "gms")
val distribution = (findProperty("orynDistribution") as String?)?.trim()?.ifEmpty { null } ?: "foss"
if (distribution !in distributions) {
    throw GradleException("orynDistribution 必须是 ${distributions.joinToString(" / ")}，收到 \"$distribution\"")
}
val distributionCap = distribution.replaceFirstChar { it.uppercase() }

if (distribution == "gms") {
    // google-services.json 含 Firebase 项目标识，不入库：本机从 .dev/m7/ 复制，CI 由 secret ANDROID_GOOGLE_SERVICES_JSON 写入
    if (!file("google-services.json").exists()) {
        throw GradleException(
            "gms 构建缺少 ${file("google-services.json").path}：" +
                "本机执行 `cp .dev/m7/google-services.json web/src-tauri/gen/android/app/`，" +
                "CI 把 secret ANDROID_GOOGLE_SERVICES_JSON 写到该路径；不需要 FCM 时用 -PorynDistribution=foss",
        )
    }
    apply(plugin = "com.google.gms.google-services")
}

val keystorePropertiesFile = rootProject.file("keystore.properties")

android {
    compileSdk = 37
    namespace = "com.lynricsy.oryn"
    defaultConfig {
        manifestPlaceholders["usesCleartextTraffic"] = "false"
        applicationId = "com.lynricsy.oryn"
        minSdk = 33
        targetSdk = 37
        versionCode = tauriProperties.getProperty("tauri.android.versionCode", "1").toInt()
        versionName = tauriProperties.getProperty("tauri.android.versionName", "1.0")
    }
    @Suppress("UnstableApiUsage")
    flavorDimensions.add("dist")
    productFlavors {
        create("foss") {
            dimension = "dist"
            buildConfigField("String", "ORYN_DISTRIBUTION", "\"foss\"")
        }
        create("gms") {
            dimension = "dist"
            buildConfigField("String", "ORYN_DISTRIBUTION", "\"gms\"")
        }
    }
    signingConfigs {
        if (keystorePropertiesFile.exists()) {
            create("release") {
                val p = Properties().apply { load(FileInputStream(keystorePropertiesFile)) }
                keyAlias = p["keyAlias"] as String
                keyPassword = p["password"] as String
                storeFile = file(p["storeFile"] as String)
                storePassword = p["password"] as String
            }
        }
    }
    buildTypes {
        getByName("debug") {
            manifestPlaceholders["usesCleartextTraffic"] = "true"
            isDebuggable = true
            isJniDebuggable = true
            isMinifyEnabled = false
            packaging {
                jniLibs.keepDebugSymbols.add("*/arm64-v8a/*.so")
                jniLibs.keepDebugSymbols.add("*/armeabi-v7a/*.so")
                jniLibs.keepDebugSymbols.add("*/x86/*.so")
                jniLibs.keepDebugSymbols.add("*/x86_64/*.so")
            }
        }
        getByName("release") {
            if (keystorePropertiesFile.exists()) signingConfig = signingConfigs.getByName("release")
            optimization {
               enable = true
            }
            proguardFiles(
                *fileTree(".") {
                  include("**/*.pro")
                  exclude("build/**")
                }.files.toTypedArray()
            )
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures {
        buildConfig = true
    }
}

androidComponents {
    beforeVariants { variant ->
        val dist = variant.productFlavors.firstOrNull { it.first == "dist" }?.second
        if (dist != null && dist != distribution) variant.enable = false
    }
}

kotlin {
    compilerOptions {
        jvmTarget = JvmTarget.JVM_17
    }
}

rust {
    rootDirRel = "../../../"
}

dependencies {
    implementation("androidx.webkit:webkit:1.14.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.activity:activity-ktx:1.10.1")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.lifecycle:lifecycle-process:2.10.0")
    "gmsImplementation"("com.google.firebase:firebase-messaging:26.0.2")
    // tauri-plugin-barcode-scanner 2.5.3 钉的 CameraX 1.5.1 遇到不认识的动态范围档位（Android 17 相机报的 8192）时
    // bindToLifecycle 抛 NPE，插件吞掉异常、相机永远不开（扫码页黑屏）；1.6 起未知档位只告警跳过。只抬下限，不改插件
    constraints {
        for (m in listOf("camera-core", "camera-camera2", "camera-lifecycle", "camera-view")) {
            implementation("androidx.camera:$m:1.6.2")
        }
    }
    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.1.4")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.5.0")
}

apply(from = file("tauri.build.gradle.kts"))

// tauri CLI 固定执行 assemble<Abi><Bt> / bundle<Abi><Bt>（Abi = Universal 或 --split-per-abi 时的各架构），
// 并到 apk/<abi>/<bt>/app-<abi>-<bt>*.apk、bundle/<abi><Bt>/app-<abi>-<bt>.aab 取产物。
// 加了 dist 维度后变体名变成 <abi><Dist><Bt>：这里补上 CLI 认的任务名，并把产物复制到 CLI 认的路径
// （原路径 apk/<abi><Dist>/… 照旧保留）。
val outputsDir = layout.buildDirectory.dir("outputs")
for (abi in listOf("universal", "arm64", "arm", "x86", "x86_64")) {
    val abiCap = abi.replaceFirstChar { it.uppercase() }
    for (bt in listOf("debug", "release")) {
        val btCap = bt.replaceFirstChar { it.uppercase() }
        val variant = "$abiCap$distributionCap$btCap"
        val copyApk = tasks.register<Copy>("orynCopy${abiCap}${btCap}Apk") {
            from(outputsDir.map { it.dir("apk/$abi$distributionCap/$bt") }) { include("*.apk") }
            into(outputsDir.map { it.dir("apk/$abi/$bt") })
            rename("^app-$abi-$distribution-(.*)$", "app-$abi-$1")
        }
        val copyAab = tasks.register<Copy>("orynCopy${abiCap}${btCap}Aab") {
            from(outputsDir.map { it.dir("bundle/$abi$distributionCap$btCap") }) { include("*.aab") }
            into(outputsDir.map { it.dir("bundle/$abi$btCap") })
            rename("^app-$abi-$distribution-(.*)$", "app-$abi-$1")
        }
        afterEvaluate {
            tasks.named("assemble$variant") { finalizedBy(copyApk) }
            tasks.named("bundle$variant") { finalizedBy(copyAab) }
            copyApk.configure { dependsOn("assemble$variant") }
            copyAab.configure { dependsOn("bundle$variant") }
            if (tasks.findByName("assemble$abiCap$btCap") == null) {
                tasks.register("assemble$abiCap$btCap") { dependsOn(copyApk) }
            }
            if (tasks.findByName("bundle$abiCap$btCap") == null) {
                tasks.register("bundle$abiCap$btCap") { dependsOn(copyAab) }
            }
        }
    }
}
