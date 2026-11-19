package persona.feature.settings

import android.content.Context
import android.content.res.Resources
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.State
import androidx.compose.runtime.mutableStateOf
import persona.feature.settings.generated.AboutVersionModel

/** Native package metadata for the experimental Version row. Build and commit stay outside this snapshot. */
@Immutable
internal data class AboutVersionSnapshot(val versionName: String?) {
    fun displayVersion(resources: Resources): String =
        versionName ?: resources.getString(R.string.settings_not_provided)

    companion object {
        /** The app owns package access; no framework lookup occurs in generated view code. */
        fun read(context: Context): AboutVersionSnapshot {
            val info = context.packageManager.getPackageInfo(context.packageName, 0)
            return AboutVersionSnapshot(info.versionName)
        }
    }
}

/**
 * A read-only implementation of the generated model boundary. Its [versionName] exposes only Compose's read-only
 * [State] interface and is initialized once from [snapshot]; no writer, lifecycle, resource lookup or subscription
 * is retained by the model. Tests construct it outside composition before presenting the generated comparison row.
 */
@Immutable
internal class ImmutableAboutVersionModel(snapshot: AboutVersionSnapshot, resources: Resources) : AboutVersionModel {
    override val versionName: State<String> = mutableStateOf(snapshot.displayVersion(resources))
}
