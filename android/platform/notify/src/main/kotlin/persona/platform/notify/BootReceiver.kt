package persona.platform.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

open class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val dependencies = Notify.dependencies(context)
        dependencies.journal().capture()
        val pending = goAsync()
        CoroutineScope(SupervisorJob() + Dispatchers.IO).launch {
            try {
                dependencies.pairing().restore()
                if (!dependencies.pairing().isPaired.value) return@launch
                ConnectionWatchdog.schedule(context)
                if (!dependencies.journal().userStopped) HubService.start(context)
            } finally {
                pending.finish()
            }
        }
    }
}
