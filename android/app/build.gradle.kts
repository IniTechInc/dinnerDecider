import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import java.util.Properties

// -----------------------------------------------------------------------------
// OWNERSHIP: this file is created once (plan Step 3) and is NOT edited by any
// later step. Everything the 66-step port needs from the build — ABI filters,
// packaging, splits, test wiring, BuildConfig, signing, and the two native
// verification tasks — is configured here up front so no later task has to touch
// a shared build file.
// -----------------------------------------------------------------------------

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.ksp)
}

// --- Local, gitignored credential/signing material ---------------------------
// Both files are optional. Absent => the feature is dormant (Kroger shows
// "unconfigured" in Settings; release builds stay unsigned) and the build still
// succeeds. Never commit either file; commit only the *.template siblings.
fun loadLocalProperties(file: File): Properties = Properties().apply {
    if (file.isFile) file.inputStream().use { load(it) }
}

val krogerPropertiesFile: File = rootProject.file("kroger.properties")
val krogerProperties: Properties = loadLocalProperties(krogerPropertiesFile)

val keystorePropertiesFile: File = rootProject.file("keystore.properties")
val keystoreProperties: Properties = loadLocalProperties(keystorePropertiesFile)
val hasReleaseKeystore: Boolean = keystorePropertiesFile.isFile &&
    keystoreProperties.getProperty("storeFile").isNullOrBlank().not()

fun krogerField(key: String, default: String = ""): String =
    (krogerProperties.getProperty(key) ?: System.getenv(key) ?: default).trim()

android {
    namespace = "com.philwoolley.dinnerdecider"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.philwoolley.dinnerdecider"
        // minSdk 28 is NOT a free choice: it is the -DANDROID_PLATFORM=android-28 the
        // verified llama.cpp cross-build linked against. Raising it is fine; lowering
        // it invalidates the native artifacts. See docs/PORTING_NOTES.md.
        minSdk = 28
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"

        // arm64-v8a ONLY. x86_64 is UNVERIFIED for llama.cpp here, and on an Apple
        // Silicon host the arm64 emulator image is the native one anyway. A device
        // without arm64-v8a gets DeviceCapabilityPolicy -> UNSUPPORTED_ABI -> mock
        // mode with the demo badge. Degraded, never crashed.
        ndk {
            abiFilters += "arm64-v8a"
        }

        // Kroger credentials come from the gitignored android/kroger.properties.
        // Empty strings are the designed-in "unconfigured" state.
        buildConfigField("String", "KROGER_CLIENT_ID", "\"${krogerField("KROGER_CLIENT_ID")}\"")
        buildConfigField("String", "KROGER_CLIENT_SECRET", "\"${krogerField("KROGER_CLIENT_SECRET")}\"")
        buildConfigField(
            "String",
            "KROGER_REDIRECT_URI",
            "\"${krogerField("KROGER_REDIRECT_URI", "dinnerdecider://kroger-callback")}\""
        )
    }

    signingConfigs {
        create("release") {
            if (hasReleaseKeystore) {
                storeFile = rootProject.file(keystoreProperties.getProperty("storeFile"))
                storePassword = keystoreProperties.getProperty("storePassword")
                keyAlias = keystoreProperties.getProperty("keyAlias")
                keyPassword = keystoreProperties.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            // proguard-rules.pro is created by plan Step 64. Running bundleRelease
            // without it fails loudly, which is the intent: shipping R8 without the
            // kotlinx.serialization / Room / JNI keep rules strips the serializers and
            // breaks the parser at runtime while every debug test still passes.
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = if (hasReleaseKeystore) signingConfigs.getByName("release") else null
        }
    }

    // One artifact, one ABI, and never a universal APK.
    //
    // AGP 8.13 REFUSES to configure when defaultConfig.ndk.abiFilters and an enabled
    // splits.abi filter name the same ABI ("Conflicting configuration : 'arm64-v8a'
    // in ndk abiFilters cannot be present when splits abi filters are set"), so only
    // one of the two mechanisms can be active. abiFilters is the one kept, because
    // it is the only one that also constrains the AAB: splits.abi is ignored by
    // bundleRelease, which plan Step 64 ships. With exactly one ABI the split would
    // produce the same single arm64-v8a artifact anyway, so nothing is lost — and
    // "no universal APK" holds trivially. The block stays, disabled and documented,
    // so nobody re-enables it without reading this.
    splits {
        abi {
            isEnable = false
            reset()
            include("arm64-v8a")
            isUniversalApk = false
        }
    }

    packaging {
        jniLibs {
            // Uncompressed + page-aligned .so, extracted-free loading. Required for
            // 16 KB page alignment to survive into the APK at targetSdk >= 35.
            useLegacyPackaging = false
        }
        resources {
            excludes += setOf(
                "/META-INF/{AL2.0,LGPL2.1}",
                "META-INF/LICENSE.md",
                "META-INF/LICENSE-notice.md"
            )
        }
    }

    // Native artifacts are produced OUT OF TREE (plan Step 34) and copied into
    // src/main/jniLibs/arm64-v8a. There is deliberately no externalNativeBuild
    // block: assembleDebug must work on a clone with no NDK, unit tests stay fast,
    // and the app build is decoupled from AGP/CMake churn.
    sourceSets["main"].jniLibs.srcDir("src/main/jniLibs")

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    testOptions {
        unitTests {
            isIncludeAndroidResources = true
            isReturnDefaultValues = true
        }
    }

    lint {
        abortOnError = true
        warningsAsErrors = false
        // MainActivity is declared in the manifest (plan Step 4, sole owner) but the
        // class itself lands with the nav host in plan Step 62. Until then lint's
        // MissingClass fires as an error and blocks `lintDebug`, which is the stated
        // validation for every early step. Demoted to a warning, not disabled: the
        // finding stays visible and self-clears once Step 62 lands.
        warning += "MissingClass"
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_17)
    }
}

ksp {
    // Room exports its schema here (plan Step 30: version = 1, exportSchema = true).
    arg("room.schemaLocation", "$projectDir/schemas")
}

// The conformance runner (plan Step 17) reads shared/conformance/cases.json directly
// from the repo rather than a copy under test resources — copying invites drift and
// makes editing-to-pass possible. This system property is how it finds the repo.
tasks.withType<Test>().configureEach {
    systemProperty("dd.shared.dir", rootProject.file("../shared").absolutePath)
}

// -----------------------------------------------------------------------------
// Native payload verification — defined INLINE, on purpose.
//
// An `apply(from = "verify-native.gradle.kts")` pointing at a file created 30+
// steps later makes Gradle throw at configuration time, which would block
// assembleDebug / lintDebug / testDebugUnitTest for every early step. So both
// tasks live here and SKIP WITH A WARNING when src/main/jniLibs/arm64-v8a is
// empty (the normal debug/mock path), while hard-failing on a release build,
// where an empty native payload means a silently vision-less app.
//
// The deep per-library criteria (seven .so, aarch64, stripped, 0x4000 LOAD
// alignment) live in android/native/verify.sh, created by plan Step 37. When that
// script exists verifyNativeLibs delegates to it; until then it performs the
// minimal presence + size-budget check below. Step 37 therefore adds a file and
// never edits this one.
// -----------------------------------------------------------------------------

// 26 MB: the real stripped payload is ~19.2 MB (six llama.cpp libraries plus
// libc++_shared.so) plus the JNI shim, with headroom. See docs/PORTING_NOTES.md.
val maxNativePayloadBytes = 26L * 1024 * 1024

// Budget for a single RELEASE artifact: the ~19 MB native payload, ML Kit's ~22 MB of
// bundled OCR/detection models (native, so R8 cannot shrink them), the shrunk dex and
// the DM Serif/DM Sans faces, with headroom.
//
// Debug artifacts are deliberately out of scope: an unminified debug APK measures ~95 MB
// here and is never shipped. Sizing the budget to accommodate it would make the check
// meaningless for the artifact that actually goes to users.
val maxArtifactBytes = 75L * 1024 * 1024

val jniLibsAbiDir: File = file("src/main/jniLibs/arm64-v8a")
val nativeVerifyScript: File = rootProject.file("native/verify.sh")
val artifactOutputsDir: File = layout.buildDirectory.dir("outputs").get().asFile

// Strict mode: anything release-shaped on the command line, or -PddStrictNativeChecks=true.
val strictNativeChecks: Boolean =
    (project.findProperty("ddStrictNativeChecks") as String?)?.toBoolean()
        ?: gradle.startParameter.taskNames.any { it.contains("Release") }

fun sharedObjectsIn(dir: File): List<File> =
    dir.listFiles()?.filter { it.isFile && it.name.endsWith(".so") }?.sortedBy { it.name }.orEmpty()

tasks.register("verifyNativeLibs") {
    group = "verification"
    description = "Checks the packaged arm64-v8a native payload (skips when jniLibs is empty)."
    doLast {
        val libs = sharedObjectsIn(jniLibsAbiDir)
        if (libs.isEmpty()) {
            val message = "verifyNativeLibs: ${jniLibsAbiDir.path} is empty — no native payload to verify."
            if (strictNativeChecks) {
                throw GradleException(
                    "$message A release build with no libmtmd.so ships an app that cannot see. " +
                        "Run the plan Step 34 native build before releasing."
                )
            }
            logger.warn("$message Skipping (debug/mock path; native libs arrive in plan Step 34).")
            return@doLast
        }

        if (nativeVerifyScript.isFile) {
            logger.lifecycle("verifyNativeLibs: delegating to ${nativeVerifyScript.path}")
            val process = ProcessBuilder("/bin/sh", nativeVerifyScript.absolutePath, jniLibsAbiDir.absolutePath)
                .redirectErrorStream(true)
                .start()
            process.inputStream.bufferedReader().forEachLine { logger.lifecycle(it) }
            val exit = process.waitFor()
            if (exit != 0) throw GradleException("verifyNativeLibs: ${nativeVerifyScript.name} failed (exit $exit).")
        } else {
            logger.lifecycle("verifyNativeLibs: ${nativeVerifyScript.path} not present yet; running minimal checks.")
            libs.forEach { lib ->
                if (lib.length() == 0L) throw GradleException("verifyNativeLibs: ${lib.name} is empty.")
                logger.lifecycle("  ${lib.name}  ${"%.1f".format(lib.length() / 1024.0 / 1024.0)} MB")
            }
        }

        val total = libs.sumOf { it.length() }
        if (total > maxNativePayloadBytes) {
            throw GradleException(
                "verifyNativeLibs: native payload is ${total / 1024 / 1024} MB, budget is " +
                    "${maxNativePayloadBytes / 1024 / 1024} MB. Did the libraries ship unstripped? " +
                    "(140 MB unstripped vs 10 MB stripped is a 14x difference.)"
            )
        }
        logger.lifecycle("verifyNativeLibs: OK — ${libs.size} libraries, ${total / 1024 / 1024} MB.")
    }
}

tasks.register("checkApkSize") {
    group = "verification"
    description = "Fails if a release APK/AAB exceeds the size budget (skips when jniLibs is empty)."
    doLast {
        if (sharedObjectsIn(jniLibsAbiDir).isEmpty()) {
            val message = "checkApkSize: ${jniLibsAbiDir.path} is empty — the artifact carries no native payload."
            if (strictNativeChecks) {
                throw GradleException("$message Refusing to size-check a release artifact that cannot run inference.")
            }
            logger.warn("$message Skipping (debug/mock path; native libs arrive in plan Step 34).")
            return@doLast
        }

        val artifacts = artifactOutputsDir.walkTopDown()
            .filter { it.isFile && (it.extension == "apk" || it.extension == "aab") }
            .filter { it.absolutePath.contains("release", ignoreCase = true) }
            .toList()
        if (artifacts.isEmpty()) {
            logger.warn(
                "checkApkSize: no release APK/AAB under ${artifactOutputsDir.path}; " +
                    "run assembleRelease or bundleRelease first."
            )
            return@doLast
        }
        artifacts.forEach { artifact ->
            val mb = "%.1f".format(artifact.length() / 1024.0 / 1024.0)
            if (artifact.length() > maxArtifactBytes) {
                throw GradleException(
                    "checkApkSize: ${artifact.name} is $mb MB, budget is ${maxArtifactBytes / 1024 / 1024} MB."
                )
            }
            logger.lifecycle("checkApkSize: ${artifact.name} $mb MB — OK.")
        }
    }
}

// Release builds must not be able to skip either check.
afterEvaluate {
    listOf("assembleRelease", "bundleRelease").forEach { name ->
        tasks.findByName(name)?.finalizedBy("verifyNativeLibs", "checkApkSize")
    }
}

dependencies {
    // --- Compose -------------------------------------------------------------
    val composeBom = platform(libs.androidx.compose.bom)
    implementation(composeBom)
    androidTestImplementation(composeBom)
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material.icons.extended)
    debugImplementation(libs.androidx.compose.ui.tooling)
    debugImplementation(libs.androidx.compose.ui.test.manifest)

    // --- App shell -----------------------------------------------------------
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.process)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.androidx.browser)
    implementation(libs.androidx.exifinterface)

    // --- Persistence ---------------------------------------------------------
    implementation(libs.androidx.room.runtime)
    implementation(libs.androidx.room.ktx)
    ksp(libs.androidx.room.compiler)
    implementation(libs.androidx.datastore.preferences)

    // --- Background work -----------------------------------------------------
    implementation(libs.androidx.work.runtime.ktx)

    // --- Networking ----------------------------------------------------------
    implementation(libs.okhttp)

    // --- Camera --------------------------------------------------------------
    implementation(libs.androidx.camera.core)
    implementation(libs.androidx.camera.camera2)
    implementation(libs.androidx.camera.lifecycle)
    implementation(libs.androidx.camera.view)

    // --- ML Kit (bundled models: must work offline at first run) --------------
    implementation(libs.mlkit.text.recognition)
    implementation(libs.mlkit.objectdetection)

    // --- Kotlin runtime ------------------------------------------------------
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)

    // --- Unit tests (host JVM; this is where the parity proof runs) ----------
    testImplementation(libs.junit)
    testImplementation(libs.robolectric)
    testImplementation(libs.androidx.test.core)
    testImplementation(libs.kotlinx.coroutines.test)
    testImplementation(libs.kotlinx.serialization.json)
    testImplementation(libs.turbine)
    testImplementation(libs.mockk)
    testImplementation(libs.androidx.room.testing)
    testImplementation(libs.androidx.work.testing)
    testImplementation(libs.okhttp.mockwebserver)

    // --- Instrumented tests --------------------------------------------------
    androidTestImplementation(libs.junit)
    androidTestImplementation(libs.androidx.test.core)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.rules)
    androidTestImplementation(libs.androidx.test.espresso.core)
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    androidTestImplementation(libs.kotlinx.coroutines.test)
    androidTestImplementation(libs.turbine)
    androidTestImplementation(libs.mockk.android)
    androidTestImplementation(libs.androidx.room.testing)
    androidTestImplementation(libs.androidx.work.testing)
}
