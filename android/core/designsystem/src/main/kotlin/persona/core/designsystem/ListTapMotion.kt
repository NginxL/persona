package persona.core.designsystem

import android.os.Handler
import android.os.Looper
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.AnimationVector
import androidx.compose.animation.core.AnimationVector1D
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.TwoWayConverter
import androidx.compose.animation.core.VectorizedFiniteAnimationSpec
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.foundation.lazy.LazyItemScope
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.MutableIntState
import androidx.compose.runtime.State
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.input.nestedscroll.NestedScrollConnection
import androidx.compose.ui.input.nestedscroll.NestedScrollSource
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.Measurable
import androidx.compose.ui.layout.MeasureResult
import androidx.compose.ui.layout.MeasureScope
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.node.LayoutAwareModifierNode
import androidx.compose.ui.node.LayoutModifierNode
import androidx.compose.ui.node.ModifierNodeElement
import androidx.compose.ui.node.ObserverModifierNode
import androidx.compose.ui.node.invalidatePlacement
import androidx.compose.ui.node.observeReads
import androidx.compose.ui.platform.InspectorInfo
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.IntOffset
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import androidx.compose.runtime.snapshotFlow
import kotlin.math.abs

/**
 * Which iPhone animation a tap copies (#9600).
 *
 * [State] is `withAnimation(Theme.Motion.standard)` (`Theme.swift` `Motion.standard` = `state`,
 * snappy 0.25 s): Agents folds (`ChatsView.swift:1168`, including a tab at `:2080`), quiet spaces
 * (`:2595`, `:3114`, `:3214`, `:3237`), Show all and idle rows (`:3010`, `:3048`, `:3074`), Codex
 * subtasks (`:3289`), Collapse All and Expand All (`AgentsMenus.swift:125`, `:128`), the computer
 * chip (`ChatsView.swift:1646`), pin / mute and a failed save (`PinsStore.swift:131`, `:139`, `:163`),
 * and Inbox folds, bulk Mark as Done, Clean and Clear all (`InboxView.swift:458`, `:634`, `:703`,
 * `:1029`, `:1127`, `:1143`), filter chips (`:1263`, `:1294`) and Clear Filters (`:768`).
 *
 * [Layout] is `withAnimation(Theme.Motion.layout)` for the Agents grouping picker
 * (`AgentsMenus.swift:405`).
 */
enum class ListTapMotionKind { State, Layout }

/**
 * Row placement for one tap. Only a row that was laid out on the frame before [arm] moves; a row
 * that comes back, or that was off screen, fades in where it lands (`withAnimation` inserts in
 * place). A second tap keeps the pixels that are still in flight. The spring runs out on its own.
 *
 * Glass uses [PersonaMotion.enter] (`Tokens.Motion.State`) or the layout spring (`Tokens.Motion.Layout`).
 * Brief and Board use those functions' tweens (`Motions`, 240 ms and 160 ms). [reduceMotion] yields
 * null specs: the rows jump, including when the system's animations are off.
 *
 * iOS passes `nil` for Reduce Motion only at `AgentsMenus.swift:405`. The other listed
 * `withAnimation(Theme.Motion.standard)` calls do not read it; this still snaps, because each caller
 * has to (`DESIGN_SYSTEM.md` 2.7) and this screen's motion is instant with animations off.
 */
class ListTapMotion {
    var kind: ListTapMotionKind? by mutableStateOf(null)
        private set
    var generation by mutableIntStateOf(0)
        private set

    /** Keys on screen the last time the list was composed. [arm] copies them. */
    private var composed: List<Any>? = null
    /** The same list, so a caller that does not pass keys still sees this composition. */
    var composedKeys by mutableStateOf<List<Any>>(emptyList())
        private set
    /** Keys at the tap, so a later list is the change to animate. */
    internal var armedBaseline: List<Any>? = null

    /**
     * The Agents page is throttled ([awaitPage]): wait until it emits, not a fixed half-second.
     * Inbox writes its list in the tap, so it leaves this false and a no-op tap drops on the next frame.
     */
    internal var awaitPage: Boolean = false
        private set

    /** Bumped when the screen's page value changes. Not a key change by itself. */
    var landings by mutableIntStateOf(0)
        private set
    internal var armedLandings: Int = 0

    /** The page build this tap is waiting for. A build from before the tap carries an older one. */
    internal var armedEpoch: Int = 0

    fun noteComposed(keys: List<Any>) {
        composed = keys
        composedKeys = keys
    }

    /**
     * The page the tap was waiting for has been composed. [epoch] is that page's build. A page
     * computed before the tap (the Agents list is throttled) is ignored, so it cannot take the flag
     * or be animated in the tap's place. Omit [epoch] (`-1`) when the caller is not a throttled page.
     */
    fun land(epoch: Int = -1) {
        if (kind != null && awaitPage && epoch >= 0 && epoch != armedEpoch) return
        landings++
        if (awaitPage && (epoch < 0 || epoch == armedEpoch)) {
            epochReady = true
            val keys = pendingKeys
            // A build from before the tap stays in [pendingKeys] until the tap's own build.
            if (!captured && keys != null && keys != armedBaseline && (pendingEpoch < 0 || pendingEpoch == armedEpoch)) capture(keys)
        }
    }

    /** A page that arrived before [land] of [armedEpoch]. Remember it; do not animate it. */
    internal fun holdKeys(keys: List<Any>, epoch: Int = -1) {
        pendingKeys = keys
        pendingEpoch = epoch
    }

    /** The list this motion draws. [arm] reads who is actually on screen from it. */
    private var listState: LazyListState? = null

    /** Last key-list signature. An idle scroll with the same signature does not rebuild keys. */
    private var storedSignature: Int? = null

    internal fun attachList(state: LazyListState) { listState = state }

    /**
     * True when this composition's keys can be the tap's diff, or the baseline does not exist yet.
     * An idle scroll does not collect them (`wantsKeys` is false and the signature matches).
     */
    internal fun wantsKeys(): Boolean = kind != null || pendingEpoch >= 0 || composed == null

    internal fun sameSignature(signature: Int): Boolean = !wantsKeys() && storedSignature == signature

    /** Rows on screen now. Null when the list has not been laid out, so [arm] falls back to [placed]. */
    private fun visibleNow(): Set<Any>? {
        val items = listState?.layoutInfo?.visibleItemsInfo ?: return null
        if (items.isEmpty()) return null
        val keys = HashSet<Any>(items.size)
        for (item in items) keys.add(item.key)
        return keys
    }

    /** Drop rows that scrolled away, so a long scroll does not keep every row that was ever placed. */
    internal fun pruneRetained(keep: Set<Any>) {
        if (keep.isEmpty()) return
        layoutY.keys.retainAll(keep)
        shiftedEpoch.keys.retainAll(keep)
        val dropShifts = shifts.keys.filter { it !in keep }
        for (key in dropShifts) {
            shifts.remove(key)?.job?.cancel()
            // Wake any still-attached observer before removing its tick state so placement returns to 0.
            ticks[key]?.let { it.intValue++ }
        }
        val dropTicks = ticks.keys.filter { it !in keep }
        for (key in dropTicks) ticks.remove(key)
        placed.retainAll(keep)
        gone.retainAll(keep)
    }

    private var prunedToken: Long = Long.MIN_VALUE

    /** Once per visible window, never mid-tap-diff. Tracks start offset, count, first and last keys. */
    private fun pruneToVisible(current: Any) {
        if (diffReady) return
        val info = listState?.layoutInfo ?: return
        val items = info.visibleItemsInfo
        if (items.isEmpty()) return
        val first = items.first().key
        val last = items.last().key
        var token = info.viewportStartOffset.toLong()
        token = 31L * token + items.size.toLong()
        token = 31L * token + first.hashCode().toLong()
        token = 31L * token + (last.hashCode().toLong() shl 32)
        if (token == prunedToken) return
        prunedToken = token
        val keep = HashSet<Any>(items.size + 1)
        for (item in items) keep.add(item.key)
        keep.add(current)
        pruneRetained(keep)
    }

    /**
     * Call on the main thread in the tap, before the state write the list will show.
     * [awaitPage] waits for [land] or a key change, up to [PAGE_WAIT_MS] (the Agents page throttle).
     * [epoch] is the build that will contain this tap's change.
     */
    fun arm(next: ListTapMotionKind, awaitPage: Boolean = false, epoch: Int = 0) {
        preparingFade = false
        armedBaseline = composed
        armedLandings = landings
        armedEpoch = epoch
        this.awaitPage = awaitPage
        captured = false
        movingKeys = emptySet()
        diffKeys = emptySet()
        diffReady = false
        commonShift = null
        pendingKeys = null
        pendingEpoch = -1
        epochReady = !awaitPage
        // Who is on screen (`visibleItemsInfo`), not every row ever placed. A row that stays composed
        // but is not in that set does not spring from a stale y.
        eligible = visibleNow() ?: if (placed.isNotEmpty()) placed.toSet() else emptySet()
        plainKind = next
        kind = next
        generation++
    }

    /** Keys whose place changed for this tap. A later hub key is not added, so it jumps. */
    var movingKeys by mutableStateOf<Set<Any>>(emptySet())
        private set

    /** True once [movingKeys] is the tap's own diff. */
    var captured by mutableStateOf(false)
        private set

    /**
     * Plain fields, not snapshot state. [capture] runs at the start of the changed frame, and the
     * rows compose later in that same pass: a snapshot write would still be the previous value,
     * so the frame that moves would have no spec and the row would jump.
     */
    private var diffKeys: Set<Any> = emptySet()
    private var diffReady: Boolean = false

    /** Not snapshot state. Item bodies read this so arming the flag does not recompose every row. */
    internal var plainKind: ListTapMotionKind? = null
        private set
    /**
     * The spring, written by [BindListTapMotion] after it recomposes. Row bodies do not read it.
     * Placement uses this when the row's own composition has not run since [arm].
     */
    internal var placedSpec: FiniteAnimationSpec<Float>? = null

    fun animates(key: Any): Boolean {
        if (plainKind == null && kind == null) return false
        if (!diffReady) return false
        // Only a key whose order changed. iOS keeps an inline title (InboxView.swift:152-153) and
        // ending a selection does not shift the list (:632), so a shift shared by every row is not
        // animated. Removed rows fade out; inserted rows fade in.
        return key in diffKeys && indexBefore[key] != indexAfter[key]
    }

    /**
     * Content y of each row (viewport y minus the scroll since it was stored), and the extra pixels
     * that draw it back toward the place it had. `animateItem`'s placement spec applies a removal
     * twice when the spec and the new offsets arrive together. This offset is the distance the row
     * actually changed, once, and only if the row was on screen the frame before the tap.
     */
    private val layoutY = mutableMapOf<Any, Float>()
    private val shifts = mutableMapOf<Any, RowShift>()
    private val shiftedEpoch = mutableMapOf<Any, Int>()
    private val placed = mutableSetOf<Any>()
    private var eligible: Set<Any> = emptySet()
    private var shiftEpoch: Int = 0
    private var indexBefore: Map<Any, Int> = emptyMap()
    private var indexAfter: Map<Any, Int> = emptyMap()
    /** `old - new` of a row whose index did not change. The large title's push, not a row spring. */
    private var commonShift: Float? = null
    /** The Agents page epoch this tap is allowed to capture. False until [land] sees [armedEpoch]. */
    internal var epochReady: Boolean = false
        private set
    /** Keys from a page that is not this tap yet, and the build they came from. -1 is a test list with no build. */
    private var pendingKeys: List<Any>? = null
    private var pendingEpoch: Int = -1
    /** The list content lambda is not composable, so it only stages. [BindListTapMotion] writes. */
    private var stagedKeys: List<Any>? = null
    private var stagedEpoch: Int = -1
    /** Per-key state, so one row's animation only invalidates that row's placement. */
    private val ticks = HashMap<Any, MutableIntState>()
    /** Added to a viewport y to undo scrolling. A drag must not look like the row changed place. */
    private var scrollAccum = 0f

    private fun tickOf(key: Any): MutableIntState = ticks.getOrPut(key) { mutableIntStateOf(0) }

    /** [consumedY] is the nested-scroll delta already applied to viewport positions (negative when the list moves up). */
    fun addViewportDelta(consumedY: Float) {
        if (consumedY == 0f) return
        scrollAccum += consumedY
    }

    /**
     * Cancels active row shifts and resets content-relative scroll baselines without disarming a
     * pending tap that is still waiting for its page (for automatic reading-position corrections
     * such as `KeepAgentReadingPosition`, which may fire on an older throttled page before the
     * tap's own `tapEpoch` arrives). If a tap diff has already been captured, disarms it so rows
     * never spring from pre-jump coordinates.
     */
    fun cancelShiftsAndResetBaseline() {
        cancelAllShifts()
        layoutY.clear()
        shiftedEpoch.clear()
        commonShift = null
        scrollAccum = 0f
        prunedToken = Long.MIN_VALUE
        eligible = emptySet()
        if (captured || diffReady) disarm()
    }

    /**
     * Call before or immediately after an explicit programmatic scroll (`scrollToItem`,
     * `requestScrollToItem`, `animateScrollToItem`) that bypasses [NestedScrollConnection].
     * Cancels active row shifts, resets content-relative baselines, and disarms any armed tap so
     * rows never spring across a viewport jump.
     */
    fun onProgrammaticScroll() {
        preparingFade = false
        cancelShiftsAndResetBaseline()
        if (kind != null || plainKind != null || diffReady) disarm()
    }

    /** Cancels every active row shift and wakes its observer so placement returns to 0 immediately. */
    fun cancelAllShifts() {
        if (shifts.isEmpty()) return
        val keys = shifts.keys.toList()
        for (key in keys) {
            shifts.remove(key)?.job?.cancel()
            poke(key)
        }
    }

    internal fun hasActiveShifts(): Boolean = shifts.isNotEmpty()

    fun shiftPx(key: Any): Int {
        tickOf(key).intValue
        val shift = shifts[key] ?: return 0
        if (shift.job.isCompleted) {
            shifts.remove(key)
            return 0
        }
        return shift.anim.value.toInt()
    }

    /** Keys disposed this frame. A row the list composes again keeps its y; one that stays gone does not. */
    private val gone = mutableSetOf<Any>()

    /**
     * The row left composition. LazyColumn can dispose and compose the same key in the change frame.
     * Clearing [layoutY] there drops the spring. The next layout pass drops a key that was not placed again.
     */
    fun forget(key: Any) {
        gone.add(key)
        placed.remove(key)
    }

    private fun resolvePlacementSpec(spec: FiniteAnimationSpec<Float>?): FiniteAnimationSpec<Float>? {
        if (themeReduce) return null
        if (plainKind == null && kind == null) return null
        if (spec != null && spec !== liveFade) return spec
        if (placedSpec != null) return placedSpec
        if (themeMotion != null) return currentFade()
        return spec
    }

    fun notePlaced(key: Any, viewportY: Float, scope: CoroutineScope, spec: FiniteAnimationSpec<Float>? = null) {
        // The list's content lambda runs in measure, after [BindListTapMotion]'s side effect.
        // Flush here so this placement sees the keys it just declared.
        flushStaged()
        pruneToVisible(key)
        gone.remove(key)
        placed.add(key)
        val y = viewportY - scrollAccum
        val current = shifts[key]
        val active = resolvePlacementSpec(spec)
        if (diffReady && eligible.isEmpty() && layoutY.isNotEmpty()) eligible = layoutY.keys.toSet()
        // The gap between arm and the list write used to drop the spring (a second tap jumped).
        if (!diffReady || active == null) {
            if (current == null) layoutY[key] = y
            return
        }
        if (key !in eligible || !animates(key)) {
            if (current != null && shiftedEpoch[key] != shiftEpoch) {
                current.job.cancel()
                shifts.remove(key)
                poke(key)
            }
            if (commonShift == null) noteAnchor(key, y)
            layoutY[key] = y
            return
        }
        if (commonShift == null) noteAnchor(key, y)
        val common = commonShift ?: 0f
        // One spring per tap. A later edit (a hub row arriving while this spring runs) snaps.
        // A scroll changes the viewport y only; [y] is content-relative, so the spring continues.
        // The list can place a row twice before the spring draws: the second pass retargets.
        if (shiftedEpoch[key] == shiftEpoch) {
            if (current != null && abs(y - current.anchoredY) > 1f) {
                val visual = current.anim.value
                current.job.cancel()
                if (abs(visual - current.initial) < 0.5f) {
                    launchShift(key, visual + (current.anchoredY - y) - (common - current.assumed), y, scope, active, common)
                } else {
                    shifts.remove(key)
                    poke(key)
                }
            }
            layoutY[key] = y
            return
        }
        val old = layoutY[key]
        val remaining = current?.anim?.value ?: 0f
        val from = if (old == null) 0f else old + remaining - y - common
        if (old != null && abs(from) > 1f) {
            shiftedEpoch[key] = shiftEpoch
            layoutY[key] = y
            current?.job?.cancel()
            launchShift(key, from, y, scope, active, common)
        }
    }

    /**
     * A row above the first index this tap changed, whose own index did not change, names the shift
     * every row shares (the large title, outside this list). A removal and an insertion above an
     * unchanged row move that row too; it is not the shared shift (`InboxView.swift:152-153`, `:632`).
     * No such row: nothing is subtracted.
     */
    private fun noteAnchor(key: Any, y: Float) {
        if (commonShift != null) return
        val before = indexBefore[key] ?: return
        if (before != indexAfter[key]) return
        val firstChanged = firstChangedIndex()
        if (firstChanged != null && before >= firstChanged) return
        val old = layoutY[key] ?: return
        val found = old - y
        commonShift = found
        val running = shifts.entries.toList()
        for ((rowKey, row) in running) {
            if (shiftedEpoch[rowKey] != shiftEpoch) continue
            if (abs(row.anim.value - row.initial) > 0.5f) continue
            val corrected = row.initial - (found - row.assumed)
            row.job.cancel()
            if (abs(corrected) <= 1f) {
                shifts.remove(rowKey)
                poke(rowKey)
            } else launchShift(rowKey, corrected, row.anchoredY, row.scope, row.spec, found)
        }
    }

    /** The earliest index a removal, insertion or move touches. Null when every index is unchanged. */
    private fun firstChangedIndex(): Int? {
        var first: Int? = null
        for (key in indexBefore.keys + indexAfter.keys) {
            val before = indexBefore[key]
            val after = indexAfter[key]
            if (before == after) continue
            val at = minOf(before ?: Int.MAX_VALUE, after ?: Int.MAX_VALUE)
            if (first == null || at < first) first = at
        }
        return first
    }

    /** The next placement is a different list than the tap's. Rows that did not move still snap. */
    internal fun seal() {
        placed.forEach { shiftedEpoch[it] = shiftEpoch }
    }

    /** Bound by [BindListTapMotion] so row springs survive transient LazyColumn item disposal during a cross-list jump. */
    internal var hostScope: CoroutineScope? = null

    private fun onMainThread(action: () -> Unit) {
        val main = runCatching { Looper.getMainLooper() }.getOrNull()
        if (main == null || Looper.myLooper() === main) action()
        else Handler(main).post(action)
    }

    private fun clearShift(key: Any, anim: Animatable<Float, AnimationVector1D>) {
        if (shifts[key]?.anim === anim) {
            shifts.remove(key)
            poke(key)
        }
    }

    private fun launchShift(
        key: Any, from: Float, y: Float, scope: CoroutineScope, spec: FiniteAnimationSpec<Float>, assumed: Float,
    ) {
        val runScope = hostScope?.takeIf { it.isActive } ?: scope
        if (!runScope.isActive) {
            shifts.remove(key)
            poke(key)
            return
        }
        val anim = Animatable(from)
        val job = runScope.launch {
            try {
                withTimeoutOrNull(MAX_SHIFT_DURATION_MS) {
                    anim.animateTo(0f, spec) { poke(key) }
                }
            } finally {
                onMainThread { clearShift(key, anim) }
            }
        }
        shifts[key] = RowShift(anim, y, job, from, assumed, runScope, spec)
        // If runScope cancelled before the coroutine body started, finally never runs; invokeOnCompletion runs immediately.
        job.invokeOnCompletion {
            onMainThread { clearShift(key, anim) }
        }
        poke(key)
    }

    private fun poke(key: Any) { tickOf(key).intValue++ }

    /** The first list after [arm]. Keys that were not in this diff are not added later. */
    fun capture(keys: List<Any>) {
        if (diffReady) return
        val before = armedBaseline.orEmpty().withIndex().associate { it.value to it.index }
        val after = keys.withIndex().associate { it.value to it.index }
        indexBefore = before
        indexAfter = after
        commonShift = null
        val diff = (before.keys + after.keys).filter { before[it] != after[it] }.toSet()
        diffKeys = diff
        diffReady = true
        movingKeys = diff
        captured = true
        shiftEpoch++
        visibleNow()?.let { pruneRetained(it) }
    }

    /**
     * The list's keys, when they might have changed or a tap is in flight. [signature] skips the
     * next idle pass that is the same list, so a scroll does not write [composedKeys].
     */
    internal fun noteKeyList(keys: List<Any>, epoch: Int, signature: Int) {
        storedSignature = signature
        stageKeys(keys, epoch)
    }

    /** Called from the list content lambda. [flushStaged] performs the snapshot write afterwards. */
    fun stageKeys(keys: List<Any>, epoch: Int) {
        stagedKeys = keys
        stagedEpoch = epoch
    }

    /** After composition, before placement. A staged page from before the tap is not the diff. */
    internal fun flushStaged() {
        val keys = stagedKeys ?: return
        val epoch = stagedEpoch
        stagedKeys = null
        commitKeys(keys, epoch)
    }

    /**
     * Every lazy key this composition declared, in order. [epoch] is the Agents page build.
     * A page from before the tap is not the diff.
     */
    fun commitKeys(keys: List<Any>, epoch: Int = -1) {
        val previous = composed
        val stale = awaitPage && kind != null && epoch >= 0 && epoch != armedEpoch
        if (stale) {
            // This older page is now what is laid out on screen before [armedEpoch] arrives;
            // keep [composed], [composedKeys], and [armedBaseline] in sync so a later no-op tap
            // disarms immediately and an earlier hub insert cannot mask the tap's index diff.
            composed = keys
            composedKeys = keys
            armedBaseline = keys
            holdKeys(keys, epoch)
            return
        }
        pendingKeys = null
        pendingEpoch = -1
        composed = keys
        val ready = !awaitPage || epoch < 0 || epoch == armedEpoch || epochReady
        if (kind != null && !captured && ready && keys != armedBaseline) capture(keys)
        else if (captured && previous != null && keys != previous) seal()
        composedKeys = keys
    }

    fun disarm() {
        preparingFade = false
        plainKind = null
        kind = null
        placedSpec = null
        captured = false
        movingKeys = emptySet()
        diffKeys = emptySet()
        diffReady = false
        epochReady = false
        pendingKeys = null
        pendingEpoch = -1
        visibleNow()?.let { pruneRetained(it) }
        // The spring still has a tail at the token duration (about 1.3% at 300 ms). Cutting it
        // jumps. Each shift removes itself when it settles.
    }

    /**
     * Enables `exitFade` (`fadeOutSpec`) for one prep frame before a local synchronous removal
     * calls [arm], without letting a hub push in that prep frame consume the placement diff.
     */
    internal var preparingFade by mutableStateOf(false)
        private set
    internal var fadePrepToken by mutableIntStateOf(0)
        private set

    fun prepareExitFade() {
        if (themeReduce || !hasTheme) return
        preparingFade = true
        fadePrepToken++
    }

    internal fun clearExitFadePrep() {
        preparingFade = false
    }

    /** How many rows still remember a place. A scroll must not keep every row that passed through. */
    internal val retainedRows: Int get() = layoutY.size

    private val stuckKeys = mutableSetOf<Any>()

    /** A sticky header currently pinned to the content padding (`InboxView.swift:827`). */
    internal fun setStuck(key: Any, value: Boolean) {
        if (value) stuckKeys.add(key) else stuckKeys.remove(key)
    }

    internal fun isStuck(key: Any): Boolean = key in stuckKeys

    /** The theme [BindListTapMotion] last drew with. [LiveFadeSpec] reads it when an animation starts. */
    internal var themeMotion: PersonaMotion? = null
    internal var themeReduce: Boolean = false
    internal var hasTheme by mutableStateOf(false)
    internal var reduceMotion by mutableStateOf(false)

    /**
     * The fade in force at this moment. Null snaps. Read from [vectorize], not from composition,
     * so arming does not recompose rows and a row composed during a tap does not keep the fade
     * (`InboxView.swift:632-634` fades the removal; a hub change is not that animation).
     */
    fun currentFade(): FiniteAnimationSpec<Float>? {
        val theme = themeMotion ?: return null
        return listTapSpecs(plainKind, theme, themeReduce).fade
    }

    /** One spec for the life of this motion. [vectorize] picks the fade when the animation starts. */
    internal val liveFade: FiniteAnimationSpec<Float> = LiveFadeSpec(this)
}

/**
 * `animateItem` keeps the spec it was given. This one's [vectorize] reads [ListTapMotion.currentFade]
 * at the start, so a removal fades only while a tap is armed and a later hub removal snaps.
 */
private class LiveFadeSpec(private val motion: ListTapMotion) : FiniteAnimationSpec<Float> {
    override fun <V : AnimationVector> vectorize(converter: TwoWayConverter<Float, V>): VectorizedFiniteAnimationSpec<V> {
        val current = motion.currentFade() ?: snap()
        return current.vectorize(converter)
    }
    override fun equals(other: Any?) = other is LiveFadeSpec && other.motion === motion
    override fun hashCode(): Int = System.identityHashCode(motion)
}

private class RowShift(
    val anim: Animatable<Float, AnimationVector1D>,
    val anchoredY: Float,
    val job: Job,
    val initial: Float,
    val assumed: Float,
    val scope: CoroutineScope,
    val spec: FiniteAnimationSpec<Float>,
)

/**
 * A synchronous tap whose keys do not change drops on the next frame. A tap that set [ListTapMotion.awaitPage]
 * waits for [ListTapMotion.land] or a key change, and no longer than [PAGE_WAIT_MS].
 * Rows whose place changed keep the spec for the spring; a key that arrives later jumps.
 *
 * [keys] is the list when the caller already has it (tests). The screen's list calls
 * [ListTapMotion.commitKeys] instead, so every lazy item is included.
 */
@Composable
fun BindListTapMotion(motion: ListTapMotion, keys: List<Any>? = null) {
    val explicitKeys = rememberUpdatedState(keys)
    fun currentKeys(): List<Any> = explicitKeys.value ?: motion.composedKeys
    val reduce = PersonaTheme.reduceMotion
    val theme = PersonaTheme.motion
    val hostScope = rememberCoroutineScope()
    // Read here, not in each row. Arming recomposes this binding and not the list.
    val fade = listTapSpecs(motion.kind, theme, reduce).fade
    SideEffect {
        motion.hostScope = hostScope
        motion.themeMotion = theme
        motion.themeReduce = reduce
        motion.hasTheme = true
        motion.reduceMotion = reduce
        motion.placedSpec = fade
    }
    DisposableEffect(motion, hostScope) {
        onDispose { if (motion.hostScope === hostScope) motion.hostScope = null }
    }
    SideEffect {
        motion.flushStaged()
        if (keys == null) return@SideEffect
        // A throttled page can compose before the tap's own build. Hold those keys; do not capture.
        if (motion.awaitPage && motion.kind != null && !motion.epochReady) {
            motion.holdKeys(keys)
            return@SideEffect
        }
        if (motion.kind != null && !motion.captured && keys != motion.armedBaseline) motion.capture(keys)
        else if (motion.captured && keys != motion.composedKeys) motion.seal()
        motion.noteComposed(keys)
    }
    LaunchedEffect(motion.fadePrepToken, reduce) {
        if (!motion.preparingFade) return@LaunchedEffect
        if (reduce) {
            motion.clearExitFadePrep()
            return@LaunchedEffect
        }
        val token = motion.fadePrepToken
        // Allow the prep frame and the following mutation frame; if nothing armed, return to idle.
        withFrameNanos { }
        withFrameNanos { }
        if (motion.fadePrepToken == token) motion.clearExitFadePrep()
    }
    LaunchedEffect(motion.generation, reduce) {
        if (motion.kind == null) return@LaunchedEffect
        if (reduce) {
            motion.disarm()
            return@LaunchedEffect
        }
        val generation = motion.generation
        val tick = motion.armedLandings
        val waiting = motion.awaitPage
        if (!waiting) {
            if (currentKeys() != motion.armedBaseline) {
                finishMovingKeys(motion, generation)
                return@LaunchedEffect
            }
            // This frame draws the spec while the rows are still where the tap found them. The list
            // write is the next frame (Inbox), so a tap that never writes drops on the one after that.
            withFrameNanos { }
            if (motion.generation != generation) return@LaunchedEffect
            if (currentKeys() != motion.armedBaseline) {
                finishMovingKeys(motion, generation)
                return@LaunchedEffect
            }
            withFrameNanos { }
            if (motion.generation != generation) return@LaunchedEffect
            if (currentKeys() == motion.armedBaseline) motion.disarm() else finishMovingKeys(motion, generation)
            return@LaunchedEffect
        }
        // Landings only. A key list from a page built before the tap must not finish this wait.
        // The landing can resume this coroutine before that frame's composition captures the diff.
        val arrived = withTimeoutOrNull(PAGE_WAIT_MS) {
            snapshotFlow { motion.landings }.first { it != tick }
        }
        if (motion.generation != generation) return@LaunchedEffect
        if (arrived == null) {
            motion.disarm()
            return@LaunchedEffect
        }
        // withFrameNanos resumes before this frame's composition. Wait out that frame, then the next,
        // so a landing cannot clear the flag before the page is captured. Compare against the current
        // [motion.armedBaseline] (which may have been rebased by an intervening stale hub page).
        repeat(2) {
            withFrameNanos { }
            if (motion.generation != generation) return@LaunchedEffect
            if (motion.captured || currentKeys() != motion.armedBaseline) {
                finishMovingKeys(motion, generation)
                return@LaunchedEffect
            }
        }
        motion.disarm()
    }
}

/**
 * Nulling the spec drops the move: the next frame draws the row at its end (measured, a reorder went
 * 280, 280, 273, then 0). The rows in [ListTapMotion.movingKeys] keep the spec for the spring.
 * A key that arrives later is not in that set, so it jumps. State is 250 ms, layout 300 ms (`Tokens.Motion`).
 * The offset itself keeps running after this returns: the spring is not cut at that duration.
 */
private suspend fun finishMovingKeys(motion: ListTapMotion, generation: Int) {
    val ms = if (motion.kind == ListTapMotionKind.Layout) Tokens.Motion.Layout.duration else Tokens.Motion.State.duration
    kotlinx.coroutines.delay(ms.toLong())
    if (motion.generation != generation) return
    motion.disarm()
    if (motion.hasActiveShifts()) {
        kotlinx.coroutines.delay(MAX_SHIFT_DURATION_MS)
        if (motion.generation == generation) motion.cancelAllShifts()
    }
}

/** Null unless a tap is in flight and motion is allowed. Same spec for placement and fade: iOS animates both. */
internal class ListTapSpecs(
    val fade: FiniteAnimationSpec<Float>?,
    val placement: FiniteAnimationSpec<IntOffset>?,
)

internal fun listTapSpecs(kind: ListTapMotionKind?, motion: PersonaMotion, reduceMotion: Boolean): ListTapSpecs {
    if (kind == null || reduceMotion) return ListTapSpecs(null, null)
    // One SwiftUI withAnimation covers both the row's opacity and its place.
    return when (kind) {
        ListTapMotionKind.State -> ListTapSpecs(motion.enter(), motion.enter())
        // Brief and Board have no layout spring; their tween is PersonaMotion's standard duration.
        ListTapMotionKind.Layout -> motion.layout?.let { ListTapSpecs(it.spec(), it.spec()) }
            ?: ListTapSpecs(tween(motion.standard, easing = FastOutSlowInEasing), tween(motion.standard, easing = FastOutSlowInEasing))
    }
}

/**
 * The list's scroll, so a row's place is content-relative. [NestedScrollConnection.onPostScroll]'s
 * consumed y is how far the viewport moved (negative when the finger drags the list up).
 */
fun listTapScroll(motion: ListTapMotion): NestedScrollConnection = object : NestedScrollConnection {
    override fun onPostScroll(consumed: Offset, available: Offset, source: NestedScrollSource): Offset {
        motion.addViewportDelta(consumed.y)
        return Offset.Zero
    }
}

/**
 * Fade goes through `animateItem` only while a tap is armed (and Reduce Motion is off). When idle,
 * all three specs are null so Compose skips `LazyLayoutAnimateItemElement` and allocates zero
 * `GraphicsLayer` / `LazyLayoutItemAnimation` instances, preventing idle `snap()` removals from
 * hitting Compose `LazyLayoutItemAnimator`'s `disappearingItems` re-entry race. Placement is a
 * layout offset read only while placing.
 */
@Composable
private fun LazyItemScope.moving(
    modifier: Modifier, motion: ListTapMotion, key: Any, pin: LazyListState?, stick: Boolean,
): Modifier {
    val scope = rememberCoroutineScope()
    val fadeInArmed = androidx.compose.runtime.remember(motion) {
        derivedStateOf { motion.kind != null && motion.hasTheme && !motion.reduceMotion }
    }
    val fadeOutArmed = androidx.compose.runtime.remember(motion) {
        derivedStateOf { (motion.kind != null || motion.preparingFade) && motion.hasTheme && !motion.reduceMotion }
    }
    DisposableEffect(motion, key) { onDispose { motion.forget(key) } }
    return exitFade(modifier.then(RowMoveElement(motion, key, scope, pin, stick)), fadeInArmed, fadeOutArmed, motion.liveFade)
}

/** Reads [fadeInArmed]/[fadeOutArmed] so only this wrapper recomposes when a tap starts or ends, not the row body. */
@Composable
private fun LazyItemScope.exitFade(
    modifier: Modifier,
    fadeInArmed: State<Boolean>,
    fadeOutArmed: State<Boolean>,
    fade: FiniteAnimationSpec<Float>,
): Modifier {
    val fadeIn = if (fadeInArmed.value) fade else null
    val fadeOut = if (fadeOutArmed.value) fade else null
    return modifier.animateItem(fadeInSpec = fadeIn, placementSpec = null, fadeOutSpec = fadeOut)
}

/**
 * Draws the row at its layout slot plus the tap offset. The offset is read in placement, and a
 * change invalidates placement only. A pinned header (its y is the viewport start) stays put
 * (`InboxView.swift:827`); an unpinned one moves (`:456`).
 */
internal class RowMoveElement(
    val motion: ListTapMotion,
    val key: Any,
    val scope: CoroutineScope,
    val pin: LazyListState?,
    val stick: Boolean,
) : ModifierNodeElement<RowMoveNode>() {
    override fun create() = RowMoveNode(motion, key, scope, pin, stick)
    override fun update(node: RowMoveNode) {
        node.update(motion, key, scope, pin, stick)
    }
    override fun equals(other: Any?): Boolean = other is RowMoveElement && other.motion === motion && other.key == key &&
        other.scope === scope && other.pin === pin && other.stick == stick
    override fun hashCode(): Int = 31 * (31 * (31 * motion.hashCode() + key.hashCode()) + (pin?.hashCode() ?: 0)) + stick.hashCode()
    override fun InspectorInfo.inspectableProperties() { name = "listTapMove" }
}

internal class RowMoveNode(
    var motion: ListTapMotion,
    var key: Any,
    var scope: CoroutineScope,
    var pin: LazyListState?,
    var stick: Boolean,
) : Modifier.Node(), LayoutModifierNode, ObserverModifierNode, LayoutAwareModifierNode {
    private var stuck = false

    fun update(
        motion: ListTapMotion,
        key: Any,
        scope: CoroutineScope,
        pin: LazyListState?,
        stick: Boolean,
    ) {
        val targetChanged = this.motion !== motion || this.key != key
        val unsticking = stuck && (targetChanged || !stick || pin == null)
        if (unsticking) {
            stuck = false
            this.motion.setStuck(this.key, false)
        }
        this.motion = motion
        this.key = key
        this.scope = scope
        this.pin = pin
        this.stick = stick
        if (isAttached && (targetChanged || unsticking)) {
            if (targetChanged) observe()
            invalidatePlacement()
        }
    }

    override fun onAttach() {
        observe()
        invalidatePlacement()
    }
    override fun onDetach() {
        if (stuck) {
            stuck = false
            motion.setStuck(key, false)
        }
    }
    override fun onObservedReadsChanged() {
        observe()
        invalidatePlacement()
    }
    private fun observe() { observeReads { motion.shiftPx(key) } }

    override fun onPlaced(coordinates: LayoutCoordinates) {
        val y = coordinates.positionInParent().y
        val info = pin?.layoutInfo
        // positionInParent includes the list's top padding. viewportStartOffset is the negative of
        // that padding, so adding the two lands on 0 and misses a header sitting under the padding.
        // The pin is the padding itself (InboxView.swift:827). reverseLayout is not used with this
        // scope; a reversed list would pin at the other end, and nothing here handles that.
        val line = info?.beforeContentPadding?.toFloat() ?: 0f
        // Only a sticky header pins (InboxView.swift:827). A row on that line still springs.
        val now = stick && pin != null && abs(y - line) <= 0.5f
        motion.setStuck(key, now)
        if (now != stuck) {
            stuck = now
            invalidatePlacement()
        }
        motion.notePlaced(key, y, scope, null)
    }

    override fun MeasureScope.measure(measurable: Measurable, constraints: Constraints): MeasureResult {
        val placeable = measurable.measure(constraints)
        return layout(placeable.width, placeable.height) {
            // Placement only. A pinned header draws in its slot; the others add the spring.
            val shift = if (stuck) 0 else motion.shiftPx(key)
            placeable.placeRelative(0, shift)
        }
    }
}

/**
 * One lazy item can emit several children (a filter bay, then a search field). The list stacks those.
 * A [androidx.compose.foundation.layout.Box] draws them on top of each other. Children are measured
 * with `minWidth = 0`, and the stack reports the widest child, as a raw lazy item does when the
 * list's minimum width is 0. Forcing the minimum up to the row width centered a short button.
 */
@Composable
private fun stack(modifier: Modifier, content: @Composable () -> Unit) {
    Layout(content, modifier) { measurables, constraints ->
        val loose = constraints.copy(minWidth = 0)
        val placeables = measurables.map { it.measure(loose) }
        val widest = placeables.maxOfOrNull { it.width } ?: 0
        val width = if (constraints.hasBoundedWidth) widest.coerceIn(constraints.minWidth, constraints.maxWidth) else widest
        val height = placeables.sumOf { it.height }
        layout(width, height) {
            var y = 0
            placeables.forEach { placeable ->
                placeable.placeRelative(0, y)
                y += placeable.height
            }
        }
    }
}

/**
 * Puts the tap spec on every row a list adds (`LazyLayoutItemAnimator` drops an in-flight move if the modifier goes).
 * [commit] publishes the keys declared in this composition, before the row bodies run.
 */
@OptIn(ExperimentalFoundationApi::class)
class ListTapLazyScope(
    private val inner: LazyListScope,
    private val motion: ListTapMotion,
    private val listState: LazyListState? = null,
    private val epoch: Int = -1,
) : LazyListScope by inner {
    init { listState?.let { motion.attachList(it) } }

    /** One lazy call. [at] is how many keys were declared before it, so two unkeyed blocks do not share a key. */
    private class Piece(val at: Int, val count: Int, val key: ((Int) -> Any)?, val explicit: Any?)
    private val pieces = mutableListOf<Piece>()
    private var running = 0

    private fun signature(): Int {
        // Every key, folded into one int. The first, middle and last keys stay put when the middle
        // is reordered, and arm() would then copy that stale list (InboxView.swift:152-153: a later
        // tap must not spring the title shift onto rows the tap did not move). No list is allocated.
        var value = epoch
        for (piece in pieces) {
            value = 31 * value + piece.at
            value = 31 * value + piece.count
            value = 31 * value + (piece.explicit?.hashCode() ?: 0)
            val key = piece.key ?: continue
            for (index in 0 until piece.count) value = 31 * value + key(index).hashCode()
        }
        return value
    }

    private fun keysOf(piece: Piece): List<Any> {
        piece.explicit?.let { return listOf(it) }
        val key = piece.key
        return List(piece.count) { index -> key?.invoke(index) ?: "item-${piece.at + index}" }
    }

    /**
     * After the items in this composition. Idle, and the same list as last time: do not walk every
     * key and do not write snapshot state (that recomposes [BindListTapMotion]). A tap, a held
     * page, or a list whose ends changed, does.
     */
    fun commit() {
        val signature = signature()
        if (motion.sameSignature(signature)) return
        val keys = ArrayList<Any>(running)
        for (piece in pieces) keys.addAll(keysOf(piece))
        motion.noteKeyList(keys, epoch, signature)
    }

    override fun item(key: Any?, contentType: Any?, content: @Composable LazyItemScope.() -> Unit) {
        val tap = motion
        val itemKey = key ?: "item-$running"
        pieces += Piece(running, 1, null, itemKey)
        running += 1
        val pin = listState
        inner.item(key, contentType) {
            val row = this
            stack(row.moving(Modifier, tap, itemKey, pin, stick = false)) { row.content() }
        }
    }

    override fun items(
        count: Int,
        key: ((index: Int) -> Any)?,
        contentType: (index: Int) -> Any?,
        itemContent: @Composable LazyItemScope.(index: Int) -> Unit,
    ) {
        val tap = motion
        val pin = listState
        val at = running
        pieces += Piece(at, count, key, null)
        running += count
        inner.items(count, key, contentType) { index ->
            val row = this
            val itemKey = key?.invoke(index) ?: "item-${at + index}"
            stack(row.moving(Modifier, tap, itemKey, pin, stick = false)) { row.itemContent(index) }
        }
    }

    override fun stickyHeader(key: Any?, contentType: Any?, content: @Composable LazyItemScope.(index: Int) -> Unit) {
        val tap = motion
        val itemKey = key ?: "header-$running"
        pieces += Piece(running, 1, null, itemKey)
        running += 1
        val pin = listState
        inner.stickyHeader(key, contentType) { index ->
            val row = this
            // One call site, pinned or not. The modifier decides in placement (InboxView.swift:827, :456).
            stack(row.moving(Modifier, tap, itemKey, pin, stick = true)) { row.content(index) }
        }
    }
}

/**
 * Agents redraws at most every 300 ms (`AgentsViewModel` `PAGE_MILLIS`). The flag waits for the
 * emission that carries the tap, and no longer than this.
 */
internal const val PAGE_WAIT_MS = 320L

/** Upper bound for any row shift spring so a stalled animation can never leave a row displaced. */
internal const val MAX_SHIFT_DURATION_MS = 1_500L
