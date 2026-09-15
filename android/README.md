# DinnerDecider - Android

Native Android port of the iOS app. Same on-device Gemma 4 E4B pipeline, same shared
contracts (`../shared/`), same design language.

## Toolchain

| | |
|---|---|
| `ANDROID_HOME` | `~/Library/Android/sdk` (**not** exported by the shell profile - export it yourself) |
| JDK | 21 (JetBrains Runtime, from Android Studio) |
| Gradle | 8.13 (wrapper, `gradle-8.13-bin.zip`) |
| AGP | 8.13.2 |
| Kotlin | 2.0.21 (KGP), KSP 2.0.21-1.0.28 |
| compileSdk / targetSdk / minSdk | 36 / 36 / 28 |
| ABI | `arm64-v8a` only |
| NDK (native build only, plan Step 34) | 28.2.13676358 (r28c), cmake 3.31.6 |

```sh
export ANDROID_HOME="$HOME/Library/Android/sdk"
export PATH="$ANDROID_HOME/platform-tools:$PATH"
```

## Commands

```sh
./gradlew :app:assembleDebug        # APK
./gradlew :app:lintDebug            # lint
./gradlew :app:testDebugUnitTest    # host-JVM suite - the parity proof, no ABI, no device
./gradlew :app:connectedDebugAndroidTest   # arm64-v8a emulator or device
./gradlew :app:verifyNativeLibs     # native payload checks (skips while jniLibs is empty)
./gradlew :app:checkApkSize         # artifact size budget (skips while jniLibs is empty)
```

## Caveats worth knowing before you change the build

### Kotlin/AGP version matrix

KGP **2.0.21** sits eight AGP minors outside Kotlin's supported matrix for **AGP 8.13**.
It is proven to work today, but it breaks the moment AGP built-in Kotlin, kapt, or AGP
screenshot tests are enabled, and it dies on AGP 9.0. Treat any AGP or Kotlin upgrade as
a **deliberate paired bump**, not a drive-by version number change.

### `minSdk 28` is load-bearing

It is the `-DANDROID_PLATFORM=android-28` the verified llama.cpp cross-build linked
against (`../docs/PORTING_NOTES.md`). Raising it is fine; lowering it invalidates the
native artifacts.

### `ndk.abiFilters` vs `splits.abi`

AGP 8.13 refuses to configure when `defaultConfig.ndk.abiFilters` and an enabled
`splits.abi` filter name the same ABI. Only `abiFilters` is active: it is the only one
of the two that also constrains the **AAB**, which `bundleRelease` ships. The `splits`
block is present but disabled, with the rationale inline in `app/build.gradle.kts`.

### No `externalNativeBuild`

The native libraries are built **out of tree** (plan Step 34) and copied into
`app/src/main/jniLibs/arm64-v8a/` (gitignored). That keeps `assembleDebug` working on a
clone with no NDK, keeps unit tests fast, and decouples the app build from AGP/CMake
churn. `verifyNativeLibs` and `checkApkSize` are defined **inline** in
`app/build.gradle.kts` and skip with a warning while `jniLibs/arm64-v8a` is empty; both
hard-fail on a release build, where an empty payload would mean a silently vision-less
app.

### Robolectric is pinned to SDK 34

`app/src/test/resources/robolectric.properties` contains `sdk=34`. Robolectric 4.13 caps
at API 34, and with `targetSdk 36` it throws
`IllegalArgumentException: Package targetSdkVersion=36 > maxSdkVersion=34`, failing
`testDebugUnitTest` 100 %. Remove the pin only together with a Robolectric 4.16+ bump.

### x86_64 is never built

A device or emulator without `arm64-v8a` gets `DeviceCapabilityPolicy -> UNSUPPORTED_ABI`
and runs in **mock mode with the demo badge**. Degraded, never crashed. On an Apple
Silicon host the arm64 emulator image is the native one, so this costs nothing.

## Local, gitignored files

| File | Purpose | Absent means |
|---|---|---|
| `kroger.properties` | `KROGER_CLIENT_ID`, `KROGER_CLIENT_SECRET`, `KROGER_REDIRECT_URI` → `BuildConfig` | Kroger integration dormant; Settings shows "unconfigured" |
| `keystore.properties` | `storeFile`, `storePassword`, `keyAlias`, `keyPassword` | release builds are left unsigned |
| `local.properties` | `sdk.dir` | `ANDROID_HOME` is used instead |
| `app/src/main/jniLibs/` | native payload from plan Step 34 | mock mode; native checks skip |

Never commit any of them. Commit only `*.template` siblings.
