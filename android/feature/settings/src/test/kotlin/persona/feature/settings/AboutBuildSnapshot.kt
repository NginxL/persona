package persona.feature.settings

import android.content.pm.PackageInfo
import androidx.compose.runtime.Immutable

/**
 * Immutable app-owned metadata for the independent generated Build-row comparison (#13001). A nullable snapshot
 * crosses the generator's supported optional scalar-input boundary; a general nullable model protocol is not
 * claimed to be supported. The [from] adapter reads native package metadata outside generated view code.
 */
@Immutable
internal data class AboutBuildSnapshot(val buildNumber: String?) {
    companion object {
        /** Android always supplies this numeric field; zero is a value, not an absent build number. */
        fun from(info: PackageInfo): AboutBuildSnapshot = AboutBuildSnapshot(info.longVersionCode.toString())
    }
}
