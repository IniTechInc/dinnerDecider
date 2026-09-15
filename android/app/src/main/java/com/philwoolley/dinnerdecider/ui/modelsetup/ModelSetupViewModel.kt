package com.philwoolley.dinnerdecider.ui.modelsetup

import android.app.ActivityManager
import android.content.Context
import android.net.Uri
import android.os.Build
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.CreationExtras
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.philwoolley.dinnerdecider.contract.DeviceCapabilityPolicy
import com.philwoolley.dinnerdecider.contract.DeviceSnapshot
import com.philwoolley.dinnerdecider.contract.MockReason
import com.philwoolley.dinnerdecider.contract.RuntimeMode
import com.philwoolley.dinnerdecider.data.PreferencesStore
import com.philwoolley.dinnerdecider.model.ImportResult
import com.philwoolley.dinnerdecider.model.ModelDownloadRepository
import com.philwoolley.dinnerdecider.model.ModelDownloadUiState
import com.philwoolley.dinnerdecider.model.ModelFileLocator
import com.philwoolley.dinnerdecider.model.ModelImporter
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * What this device can do with the model, and the numbers to say it with.
 *
 * @param mode the verdict from the pure [DeviceCapabilityPolicy].
 * @param totalMemBytes `ActivityManager.MemoryInfo.totalMem`, carried so the refusal
 *   copy can show the actual figure rather than a vague "not supported".
 */
data class ModelSetupCapability(
    val mode: RuntimeMode = RuntimeMode.Real,
    val totalMemBytes: Long = 0L,
    val availMemBytes: Long = 0L,
) {
    /** Non-null when the capability check has already refused this device. */
    val refusal: MockReason?
        get() = (mode as? RuntimeMode.Mock)?.reason

    /**
     * Whether it makes sense to offer the download at all.
     *
     * [MockReason.NO_MODEL_FILES] is exactly the state this screen exists to fix, so
     * it is not a refusal here. Everything else is: offering a 4 GB download to a
     * device that will then refuse to load it is the failure this flag prevents.
     */
    val mayOfferDownload: Boolean
        get() = refusal == null || refusal == MockReason.NO_MODEL_FILES
}

/** Everything `ModelSetupScreen` renders. */
data class ModelSetupUiState(
    val download: ModelDownloadUiState = ModelDownloadUiState(),
    val capability: ModelSetupCapability = ModelSetupCapability(),
    /** Weights files visible to the locator, for the picker. */
    val availableWeights: List<String> = emptyList(),
    /** The user's pinned weights filename, or null for "choose automatically". */
    val pinnedWeights: String? = null,
    val isImporting: Boolean = false,
    /** One-shot result copy for the import card. */
    val importMessage: String? = null,
    /** Set when the runtime notification permission was asked for and denied. */
    val notificationsDenied: Boolean = false,
)

/**
 * State holder for the model setup screen.
 *
 * ## The capability question this asks is deliberately hypothetical
 *
 * [DeviceCapabilityPolicy.evaluate] normally answers "should the app load the model
 * right now", and on a device with no weights it short-circuits at
 * [MockReason.NO_MODEL_FILES] before it ever looks at RAM. That is the wrong question
 * for this screen, which needs to know **whether a 4 GB download would be worth
 * making**. So the snapshot is built with `modelFilesPresent = true`: if we had the
 * files, would this device run them?
 *
 * On the only physical device available - a 6 GB Galaxy XCover6 Pro, whose `totalMem`
 * lands near 5.4 to 5.7 GiB against a 7.0 GiB floor - the answer is
 * `Mock(LOW_RAM_DEVICE)`, and the screen shows the reason and the RAM figure instead
 * of a download button. That is the check working, not a bug.
 *
 * `nativeLibsPresent = true` is likewise an assumption, and a documented one: the
 * `.so` load order belongs to the LLM runtime, not to this package. The definitive
 * verdict is the service container's; this is a precheck whose only job is to avoid
 * promising a download that cannot pay off.
 */
class ModelSetupViewModel(
    private val repository: ModelDownloadRepository,
    private val locator: ModelFileLocator,
    private val importer: ModelImporter,
    private val preferences: PreferencesStore,
    private val deviceProbe: () -> ModelSetupCapability,
) : ViewModel() {

    private val _state = MutableStateFlow(ModelSetupUiState())
    val state: StateFlow<ModelSetupUiState> = _state.asStateFlow()

    init {
        viewModelScope.launch {
            repository.uiState.collect { download -> _state.update { it.copy(download = download) } }
        }
        viewModelScope.launch {
            preferences.selectedModelFileName.collect { pin ->
                _state.update { it.copy(pinnedWeights = pin) }
            }
        }
        refresh()
    }

    /**
     * Re-read the device and the filesystem.
     *
     * Called on every visit, because files can arrive by routes this screen knows
     * nothing about, and because available memory moves.
     */
    fun refresh() {
        repository.refreshPresence()
        _state.update {
            it.copy(
                capability = runCatching { deviceProbe() }.getOrDefault(ModelSetupCapability()),
                availableWeights = runCatching { locator.availableModelFileNames() }
                    .getOrDefault(emptyList()),
            )
        }
    }

    /**
     * Begin the download.
     *
     * Refused outright when the capability check has already said this device cannot
     * host the model. The screen does not show the button in that case; this is the
     * second lock on the same door, because a 4 GB transfer that ends in a demo badge
     * is worse than no transfer at all.
     */
    fun start(userConfirmedMetered: Boolean = false) {
        if (!_state.value.capability.mayOfferDownload) return
        repository.start(userConfirmedMetered)
    }

    fun pause() = repository.pause()

    fun resume() = repository.resume()

    fun cancel() = repository.cancel()

    fun retry() = repository.retry()

    fun dismissMeteredConfirmation() = repository.dismissMeteredConfirmation()

    /** Record whether the runtime notification permission was granted. */
    fun onNotificationPermissionResult(granted: Boolean) {
        _state.update { it.copy(notificationsDenied = !granted) }
    }

    /** Pin a weights file, or pass null to go back to choosing automatically. */
    fun pinWeights(fileName: String?) {
        viewModelScope.launch { preferences.setSelectedModelFileName(fileName) }
    }

    /** Import a `.gguf` the user picked through the Storage Access Framework. */
    fun import(uri: Uri) {
        if (_state.value.isImporting) return
        _state.update { it.copy(isImporting = true, importMessage = null) }
        viewModelScope.launch {
            val result = runCatching { importer.import(uri) }.getOrElse { error ->
                ImportResult.Failed(error.message ?: "The import did not finish.")
            }
            val message = when (result) {
                is ImportResult.Success ->
                    "${result.file.name} was imported and checked."
                is ImportResult.Rejected -> result.reason
                is ImportResult.Failed -> result.reason
            }
            _state.update { it.copy(isImporting = false, importMessage = message) }
            refresh()
        }
    }

    fun dismissImportMessage() {
        _state.update { it.copy(importMessage = null) }
    }

    companion object {

        /** Build the real [ModelSetupCapability] probe for a [Context]. */
        fun capabilityProbe(context: Context, locator: ModelFileLocator): () -> ModelSetupCapability = {
            val manager = context.getSystemService(Context.ACTIVITY_SERVICE) as? ActivityManager
            val info = ActivityManager.MemoryInfo().also { manager?.getMemoryInfo(it) }
            val snapshot = DeviceSnapshot(
                isLowRamDevice = manager?.isLowRamDevice ?: false,
                totalMemBytes = info.totalMem,
                availMemBytes = info.availMem,
                supportedAbis = Build.SUPPORTED_ABIS?.toList().orEmpty(),
                freeDiskBytes = locator.freeDiskBytes(),
                // Assumed present: the .so load order belongs to the LLM runtime, not
                // to this package. See the class KDoc.
                nativeLibsPresent = true,
                // Hypothetical on purpose: "if we downloaded them, would this run?"
                modelFilesPresent = true,
                userForcedReal = false,
            )
            ModelSetupCapability(
                mode = DeviceCapabilityPolicy.evaluate(snapshot),
                totalMemBytes = info.totalMem,
                availMemBytes = info.availMem,
            )
        }

        /**
         * Factory for the screen.
         *
         * @param allowExternalModelsDir the developer toggle from plan Step 58's
         *   settings. Default off, so the picker shows only files in `filesDir`.
         */
        fun factory(
            context: Context,
            allowExternalModelsDir: Boolean = false,
        ): ViewModelProvider.Factory {
            val appContext = context.applicationContext
            return viewModelFactory {
                initializer<ModelSetupViewModel> {
                    val locator = ModelFileLocator.create(appContext, allowExternalModelsDir)
                    ModelSetupViewModel(
                        repository = ModelDownloadRepository.get(appContext, allowExternalModelsDir),
                        locator = locator,
                        importer = ModelImporter.create(appContext),
                        preferences = PreferencesStore(appContext),
                        deviceProbe = capabilityProbe(appContext, locator),
                    )
                }
            }
        }
    }
}
