package persona.platform.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.app.RemoteInput
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import java.util.concurrent.atomic.AtomicBoolean
import persona.core.contracts.api.EXTENSION_NOTIFICATION_REMOTE_INPUT
import persona.core.contracts.api.ExtensionNotificationActionRecord
import persona.core.contracts.api.ExtensionNotificationActionStore
import persona.core.contracts.api.ExtensionNotificationClaim

/** Background extension buttons only: claim, one bounded execution, one goAsync completion. */
class ExtensionNotificationActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION) return
        val token = intent.data?.getQueryParameter("token") ?: return
        val text = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(EXTENSION_NOTIFICATION_REMOTE_INPUT)?.toString()
        val dependencies = Notify.dependencies(context)
        val pending = goAsync()
        val completion = ExtensionNotificationReceiverCompletion()
        CoroutineScope(SupervisorJob() + Dispatchers.IO).launch {
            var record: ExtensionNotificationActionRecord? = null
            try {
                dependencies.pairing().restore()
                val store = ExtensionNotificationActionStore.shared
                val current = store.peek(token)
                record = current
                val action = current?.action
                val claim = when {
                    current == null -> ExtensionNotificationClaim.Missing
                    action == null -> ExtensionNotificationClaim.Invalid(current)
                    else -> store.claim(token, action.id,
                        if (action.input == null) emptyMap() else mapOf("text" to text.orEmpty()))
                }
                when (claim) {
                    is ExtensionNotificationClaim.Claimed -> {
                        when (val dispatch = runExtensionNotificationWithDeadline(claim,
                            ExtensionNotificationActionExecutor(dependencies.extensionNotifications()))) {
                            is ExtensionNotificationDispatch.Completed -> dispatch.report?.let(dependencies.notifier()::notifyExtensionReport)
                            is ExtensionNotificationDispatch.Timeout -> dependencies.notifier().notifyExtensionTimeout(dispatch.record)
                        }
                    }
                    is ExtensionNotificationClaim.AlreadyHandled -> Unit
                    is ExtensionNotificationClaim.Missing -> dependencies.notifier().notifyExtensionMissing(token)
                    else -> dependencies.notifier().notifyExtensionClaimFailure(claim)
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                record?.let(dependencies.notifier()::notifyExtensionTimeout)
                    ?: dependencies.notifier().notifyExtensionMissing(token)
            } finally {
                completion.finish(pending::finish)
            }
        }
    }

    companion object {
        const val ACTION = "persona.notify.extension"
    }
}

internal class ExtensionNotificationReceiverCompletion {
    private val completed = AtomicBoolean()
    fun finish(done: () -> Unit) {
        if (completed.compareAndSet(false, true)) done()
    }
}
