package persona.platform.shortcuts

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.launch
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.JSpinner
import persona.core.designsystem.PersonaTheme

/** A foreground, system-authorized hand-off from the tile to the restored fleet's destination. */
class TileRouteActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val dependencies = SurfacesDependencies.of(this)
        val style = AppStyleStore(this).style.value
        setContent {
            ShortcutTheme(style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.scrim), contentAlignment = Alignment.Center) {
                    JSpinner()
                }
            }
        }
        lifecycleScope.launch {
            val feed = dependencies.fleet()
            feed.settled()
            startActivity(SurfaceLinks.fleet(this@TileRouteActivity, feed.paired.value, feed.state.value) {
                dependencies.sessions().assistant(it)
            })
            finish()
        }
    }
}
