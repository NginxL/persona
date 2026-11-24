package persona.core.domain.models

import persona.core.contracts.Session

/**
 * [this] row with what a summary row leaves out taken from [old], the same session's row held before (iOS
 * `preservingDetails`; docs/protocol/protocol.md "Summary rows"): a summary (one with `waiting`) carries the list's
 * fields and a Persona chat's `model_pick`, but not the launcher, the permission mode, the compaction, the waiting
 * launcher switch and the other chat-only details. Without them the model pill would read "Following the default" while
 * the hub runs the chat's own pick on its launcher. A complete row, or no row held, is taken as it is; what the summary
 * does carry (the model, the effort, `model_pick`) is the summary's.
 */
fun Session.keepingDetails(old: Session?): Session {
    if (waiting == null || old == null || old.key != key) return this
    return copy(
        launcher = launcher ?: old.launcher,
        permissionMode = permissionMode ?: old.permissionMode,
        bypassAvailable = if (permissionMode == null) old.bypassAvailable else bypassAvailable,
        effort = effort ?: old.effort,
        efforts = efforts.ifEmpty { old.efforts },
        ultracode = ultracode ?: old.ultracode,
        background = background ?: old.background,
        subagents = subagents ?: old.subagents,
        workflows = workflows ?: old.workflows,
        queued = queued ?: old.queued,
        ports = ports ?: old.ports,
        suggestion = suggestion ?: old.suggestion,
        suggestionRisky = if (suggestion == null) old.suggestionRisky else suggestionRisky,
        codexRoute = codexRoute ?: old.codexRoute,
        codexRouteNote = codexRouteNote ?: old.codexRouteNote,
        runtime = runtime ?: old.runtime,
        runtimeFallback = runtimeFallback ?: old.runtimeFallback,
        modelFallback = modelFallback ?: old.modelFallback,
        modelRestore = modelRestore ?: old.modelRestore,
        compaction = compaction ?: old.compaction,
        asks = asks ?: old.asks,
        pendingSwitch = pendingSwitch ?: old.pendingSwitch,
        // Summary rows omit the coded start details. Keep the pair only while the failure they explain is unchanged;
        // a row carrying a new code must never inherit the previous code's arguments.
        startErrorCode = startErrorCode ?: old.startErrorCode.takeIf { startError == old.startError },
        startErrorArgs = if (startErrorCode != null) startErrorArgs else old.startErrorArgs.takeIf { startError == old.startError },
        undelivered = undelivered ?: old.undelivered,
    )
}
