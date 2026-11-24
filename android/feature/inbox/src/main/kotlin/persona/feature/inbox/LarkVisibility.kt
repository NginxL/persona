package persona.feature.inbox

import androidx.compose.ui.geometry.Rect
import persona.core.inbox.*

/** Keep the gate independent of LazyColumn so the shared list can supply the same inputs. */
internal fun larkReadAllowed(resumed: Boolean, focused: Boolean, positioned: Boolean, occluded: Boolean,
    scrolling: Boolean, verified: Boolean): Boolean = resumed && focused && positioned && !occluded && !scrolling && verified

/**
 * Messages actually drawn inside the viewport. Opening an item from the inbox reports its first screen as well
 * (ux.md 3.6, decided 2026-10-31); [larkReadAllowed] still decides whether anything may be reported.
 */
internal fun larkSeen(messages: List<LarkMessage>, bounds: Map<String, Rect>, viewport: Rect): List<LarkMessage> =
    messages.filter { message ->
        bounds[message.listKey()]?.let { box -> box.width > 0 && box.height > 0 && viewport.overlaps(box) } == true
    }
