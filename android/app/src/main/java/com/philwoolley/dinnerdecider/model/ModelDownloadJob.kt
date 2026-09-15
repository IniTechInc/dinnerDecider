package com.philwoolley.dinnerdecider.model

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.job.JobInfo
import android.app.job.JobParameters
import android.app.job.JobScheduler
import android.app.job.JobService
import android.content.ComponentName
import android.content.Context
import android.content.pm.ServiceInfo
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.philwoolley.dinnerdecider.contract.ModelDownloadFile
import com.philwoolley.dinnerdecider.contract.ModelDownloadLogic
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

/**
 * How [ModelDownloadRepository] asks for the transfer to start or stop.
 *
 * An interface rather than a direct call into [ModelTransferScheduler] so the
 * repository holds no `Context` at all. That is what lets the whole gating and
 * state-machine layer run on the host JVM, which is the only place any of it can be
 * proved: neither JobScheduler nor WorkManager exists there.
 */
interface TransferScheduling {
    fun schedule(allowMetered: Boolean)
    fun cancel()

    companion object {
        /** The real implementation, bound to an application context. */
        fun forContext(context: Context): TransferScheduling {
            val appContext = context.applicationContext
            return object : TransferScheduling {
                override fun schedule(allowMetered: Boolean) =
                    ModelTransferScheduler.schedule(appContext, allowMetered)

                override fun cancel() = ModelTransferScheduler.cancel(appContext)
            }
        }

        /** A scheduling target that does nothing. For previews and host-JVM tests. */
        fun noOp(): TransferScheduling = object : TransferScheduling {
            override fun schedule(allowMetered: Boolean) = Unit
            override fun cancel() = Unit
        }
    }
}

/**
 * Background execution for the model transfer.
 *
 * ## Why this is not a plain WorkManager `dataSync` foreground worker
 *
 * The obvious shape - a `CoroutineWorker` calling `setForeground` with a `dataSync`
 * type - is the wrong one for a 3.46 GB download, for three compounding reasons:
 *
 * 1. **WorkManager 2.9.1 has no `onTimeout` handling at all.** The catalog pins
 *    **2.10.0** for exactly this reason.
 * 2. **Android 15 imposes a pooled 6 h / 24 h cap on `dataSync` foreground
 *    services.** Cross the cap and the system stops the service; a worker that does
 *    not handle the timeout takes a fatal `RemoteServiceException`, which 2.9.1 is
 *    structurally unable to avoid.
 * 3. **`ForegroundServiceStartNotAllowedException` blocks a background restart**, so
 *    a transfer that stops while the app is not visible cannot simply start itself
 *    again.
 *
 * The framework's answer to "a long user-visible transfer" is a **JobScheduler
 * user-initiated data transfer job**: `setUserInitiated(true)` plus the
 * `RUN_USER_INITIATED_JOBS` permission. UIDT jobs are exempt from the `dataSync`
 * cap and get the system's own transfer notification treatment.
 *
 * **WorkManager cannot express UIDT**, not even at 2.12.0-rc01, so this is
 * JobScheduler directly. WorkManager is kept for what it is good at here: the small
 * post-download verification and bookkeeping ([ModelVerificationWorker]).
 *
 * ## The pre-API-34 path
 *
 * `setUserInitiated` arrives in API 34 and the app ships `minSdk 28`. Below 34 the
 * transfer runs as a WorkManager foreground worker ([ModelTransferWorker]) - which is
 * sound there precisely because the reasons above are API 34 and 35 problems: the
 * pooled `dataSync` cap is Android 15, and `RUN_USER_INITIATED_JOBS` did not exist
 * yet. This is the one place WorkManager runs a transfer, and it is a fallback, not
 * the design.
 */
object ModelTransferScheduler {

    /** JobScheduler id for the transfer. Stable so a reschedule replaces rather than duplicates. */
    const val JOB_ID: Int = 4101

    /** Unique WorkManager name for the pre-API-34 transfer fallback. */
    const val TRANSFER_WORK_NAME: String = "dinnerdecider-model-transfer"

    /** Unique WorkManager name for the post-download verification. */
    const val VERIFY_WORK_NAME: String = "dinnerdecider-model-verify"

    private const val TAG = "ModelTransfer"

    /**
     * Schedule the transfer.
     *
     * @param allowMetered whether the user has confirmed a metered download. When
     *   false the job carries a `NOT_METERED` capability so the system itself will
     *   not run it on a metered connection. The confirmation decision is
     *   [ModelDownloadLogic.mayStartDownload]'s, made before we get here; this is the
     *   system-level enforcement of the same answer.
     */
    fun schedule(context: Context, allowMetered: Boolean) {
        val appContext = context.applicationContext
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            if (scheduleUserInitiated(appContext, allowMetered)) return
            Log.w(TAG, "UIDT job was refused; falling back to a foreground worker.")
        }
        scheduleWorker(appContext, allowMetered)
    }

    /** Stop the transfer, whichever mechanism is carrying it. */
    fun cancel(context: Context) {
        val appContext = context.applicationContext
        runCatching {
            (appContext.getSystemService(Context.JOB_SCHEDULER_SERVICE) as? JobScheduler)?.cancel(JOB_ID)
        }
        runCatching { WorkManager.getInstance(appContext).cancelUniqueWork(TRANSFER_WORK_NAME) }
    }

    private fun scheduleUserInitiated(appContext: Context, allowMetered: Boolean): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return false
        val scheduler = appContext.getSystemService(Context.JOB_SCHEDULER_SERVICE) as? JobScheduler
            ?: return false

        val network = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
            .apply { if (!allowMetered) addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) }
            .build()

        val remainingBytes = ModelDownloadFile.entries.sumOf { it.approxBytes }
        val info = JobInfo.Builder(
            JOB_ID,
            ComponentName(appContext, ModelTransferJobService::class.java),
        )
            .setUserInitiated(true)
            .setRequiredNetwork(network)
            .setEstimatedNetworkBytes(JobInfo.NETWORK_BYTES_UNKNOWN.toLong(), remainingBytes)
            .build()

        // schedule() throws when the app is not in a state allowed to start a
        // user-initiated job (backgrounded with no exemption). That is a legitimate
        // outcome, not a crash: fall back rather than take the app down.
        return runCatching { scheduler.schedule(info) == JobScheduler.RESULT_SUCCESS }
            .getOrElse { error ->
                Log.w(TAG, "Could not schedule the user-initiated transfer job.", error)
                false
            }
    }

    private fun scheduleWorker(appContext: Context, allowMetered: Boolean) {
        val constraints = androidx.work.Constraints.Builder()
            .setRequiredNetworkType(
                if (allowMetered) androidx.work.NetworkType.CONNECTED else androidx.work.NetworkType.UNMETERED
            )
            .build()
        val request = OneTimeWorkRequestBuilder<ModelTransferWorker>()
            .setConstraints(constraints)
            .build()
        runCatching {
            WorkManager.getInstance(appContext)
                .enqueueUniqueWork(TRANSFER_WORK_NAME, ExistingWorkPolicy.REPLACE, request)
        }.onFailure { Log.w(TAG, "Could not enqueue the fallback transfer worker.", it) }
    }
}

/**
 * The user-initiated data transfer job.
 *
 * The class name is fixed by the manifest, which declares
 * `.model.ModelTransferJobService` up front so no later task has to edit a shared
 * file. Exported false, guarded by `BIND_JOB_SERVICE`.
 *
 * Holds **no decision logic**: it starts a coroutine, hands control to
 * [ModelDownloadRepository.runTransfer], keeps the system notification current, and
 * decides only whether to ask for a reschedule.
 */
class ModelTransferJobService : JobService() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var transferJob: Job? = null
    private var notificationJob: Job? = null

    override fun onStartJob(params: JobParameters): Boolean {
        val repository = ModelDownloadRepository.get(applicationContext)
        ModelDownloadNotifications.ensureChannel(applicationContext)
        publishNotification(params, repository, initial = true)

        // API 34+ requires the notification within a few seconds of the job starting,
        // so keep it fresh from the same state the screen renders.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            notificationJob = scope.launch {
                var lastPercent = -1
                while (true) {
                    val state = repository.uiState.value
                    val percent = (state.overallFraction * 100).toInt()
                    if (percent != lastPercent) {
                        lastPercent = percent
                        publishNotification(params, repository, initial = false)
                    }
                    kotlinx.coroutines.delay(NOTIFICATION_REFRESH_MILLIS)
                }
            }
        }

        transferJob = scope.launch {
            val downloaded = runCatching { repository.runTransfer() }.getOrElse { error ->
                if (error is kotlinx.coroutines.CancellationException) throw error
                Log.w(TAG, "The model transfer stopped unexpectedly.", error)
                repository.reportFailure(
                    "The download stopped unexpectedly. Tap Retry to continue where it left off."
                )
                false
            }
            notificationJob?.cancel()
            if (downloaded) ModelVerificationWorker.enqueue(applicationContext)
            // Never ask for a reschedule after a failure the user has been told about:
            // the state machine is already showing Failed with a Retry button, and a
            // silent system retry behind it would fight the UI.
            jobFinished(params, false)
        }
        return true
    }

    override fun onStopJob(params: JobParameters): Boolean {
        transferJob?.cancel()
        notificationJob?.cancel()
        // Reschedule only if the user still wants the transfer. A pause or a cancel
        // has already moved the state away from Downloading.
        return runCatching { ModelDownloadRepository.get(applicationContext).isActive() }
            .getOrDefault(false)
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun publishNotification(
        params: JobParameters,
        repository: ModelDownloadRepository,
        initial: Boolean,
    ) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return
        val notification = ModelDownloadNotifications.transferNotification(
            context = applicationContext,
            state = repository.uiState.value,
            indeterminate = initial,
        )
        runCatching {
            setNotification(
                params,
                ModelDownloadNotifications.TRANSFER_NOTIFICATION_ID,
                notification,
                JobService.JOB_END_NOTIFICATION_POLICY_REMOVE,
            )
        }.onFailure { Log.w(TAG, "Could not attach the transfer notification.", it) }
    }

    private companion object {
        const val TAG = "ModelTransferJob"
        const val NOTIFICATION_REFRESH_MILLIS = 1_000L
    }
}

/**
 * Pre-API-34 transfer fallback.
 *
 * Runs the identical [ModelDownloadRepository.runTransfer] as a foreground worker.
 * Not the design (see [ModelTransferScheduler]) but sound on API 28 to 33, where the
 * pooled `dataSync` cap that makes this shape dangerous on Android 15 does not exist.
 */
class ModelTransferWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val repository = ModelDownloadRepository.get(applicationContext)
        runCatching { setForeground(getForegroundInfo()) }
            .onFailure { Log.w(TAG, "Could not move the transfer worker to the foreground.", it) }

        val downloaded = runCatching { repository.runTransfer() }.getOrElse { error ->
            if (error is kotlinx.coroutines.CancellationException) throw error
            repository.reportFailure(
                "The download stopped unexpectedly. Tap Retry to continue where it left off."
            )
            false
        }
        if (downloaded) ModelVerificationWorker.enqueue(applicationContext)
        return if (downloaded) Result.success() else Result.failure()
    }

    override suspend fun getForegroundInfo(): ForegroundInfo {
        ModelDownloadNotifications.ensureChannel(applicationContext)
        val notification = ModelDownloadNotifications.transferNotification(
            context = applicationContext,
            state = ModelDownloadRepository.get(applicationContext).uiState.value,
            indeterminate = false,
        )
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ForegroundInfo(
                ModelDownloadNotifications.TRANSFER_NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            ForegroundInfo(ModelDownloadNotifications.TRANSFER_NOTIFICATION_ID, notification)
        }
    }

    private companion object {
        const val TAG = "ModelTransferWorker"
    }
}

/**
 * The post-download verification and bookkeeping, which is WorkManager's job here.
 *
 * Small, quick, and worth retrying on its own: it re-checks that both halves of the
 * pair are on disk at their exact sizes and that the locator resolves them, then
 * moves the state machine to Done. Kept out of the transfer job so a verification
 * that has to wait (for instance while the filesystem settles) does not hold a
 * user-initiated job open.
 */
class ModelVerificationWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val ok = ModelDownloadRepository.get(applicationContext).verifyDownloadedFiles()
        return if (ok) Result.success() else Result.failure()
    }

    companion object {
        /** Enqueue the verification pass. Replaces any pending one. */
        fun enqueue(context: Context) {
            val request = OneTimeWorkRequestBuilder<ModelVerificationWorker>().build()
            runCatching {
                WorkManager.getInstance(context.applicationContext).enqueueUniqueWork(
                    ModelTransferScheduler.VERIFY_WORK_NAME,
                    ExistingWorkPolicy.REPLACE,
                    request,
                )
            }
        }
    }
}

/**
 * The transfer's notification channel and notification.
 *
 * On API 33+ posting this needs the runtime `POST_NOTIFICATIONS` grant, which the
 * setup screen asks for **before** starting the transfer: a user-initiated data
 * transfer job without a visible notification is not something the system will run.
 */
object ModelDownloadNotifications {

    /** Channel id for model download progress. */
    const val CHANNEL_ID: String = "model_download"

    /** Notification id for the in-flight transfer. */
    const val TRANSFER_NOTIFICATION_ID: Int = 4101

    /** Create the channel if it is not already there. Cheap and idempotent. */
    fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            "Model download",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Progress while the on-device Gemma 4 files download."
            setShowBadge(false)
        }
        manager.createNotificationChannel(channel)
    }

    /**
     * Build the progress notification.
     *
     * Deliberately built with the platform [Notification.Builder] rather than
     * `NotificationCompat`: the app has no `appcompat` dependency, `minSdk 28` is
     * already past the channel cutoff, and nothing here needs a compat shim.
     */
    fun transferNotification(
        context: Context,
        state: ModelDownloadUiState,
        indeterminate: Boolean,
    ): Notification {
        val percent = (state.overallFraction * 100).toInt().coerceIn(0, 100)
        val fileName = state.currentFile?.displayName
        val text = when {
            indeterminate -> "Starting the download"
            fileName != null -> "$fileName - $percent%"
            else -> "$percent%"
        }

        val launchIntent = context.packageManager.getLaunchIntentForPackage(context.packageName)
        val contentIntent = launchIntent?.let {
            PendingIntent.getActivity(
                context,
                0,
                it,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
            )
        }

        return Notification.Builder(context, CHANNEL_ID)
            .setContentTitle("Downloading the on-device model")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setProgress(100, percent, indeterminate)
            .apply { contentIntent?.let { setContentIntent(it) } }
            .build()
    }
}
