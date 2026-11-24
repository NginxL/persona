package persona.feature.machines

import android.content.Context
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext

/*
 * The account types this phone leaves out of "Subscription limits" (#13173). The hub's own list (`usage.hidden_providers`, #11767) is
 * written from the admin page only, which a phone's device credentials cannot reach (docs/operations/admin.md), so this is a display
 * filter kept on the phone: it can hide more than the hub does, never show what the hub hides.
 */

private const val PREFS = "usage_view"
private const val HIDDEN = "hidden_providers"

internal fun readHiddenUsageProviders(context: Context): Set<String> =
    context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getStringSet(HIDDEN, emptySet()).orEmpty().toSet()

internal fun writeHiddenUsageProviders(context: Context, hidden: Set<String>) {
    context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putStringSet(HIDDEN, hidden).apply()
}

/** The hidden account types and the way to show or hide one; the choice survives the app. */
@Composable
internal fun rememberHiddenUsageProviders(): Pair<Set<String>, (String, Boolean) -> Unit> {
    val context = LocalContext.current.applicationContext
    var hidden by remember { mutableStateOf(readHiddenUsageProviders(context)) }
    return hidden to { provider, show ->
        hidden = if (show) hidden - provider else hidden + provider
        writeHiddenUsageProviders(context, hidden)
    }
}
