package persona.feature.settings

import android.content.res.Resources
import persona.core.contracts.api.ExtensionOpStep
import persona.core.contracts.api.ExtensionOpTool
import persona.core.contracts.api.ExtensionOperation
import java.time.Instant
import java.util.Locale

// An extension's install, upgrade or removal in steps (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress", #3277): the words and
// rules of its step list. The model is CatalogExtensionModel, the view ExtensionOperationView.kt.

internal object OperationText {
    /** The steps worth showing: those it skips are left out. */
    fun shown(op: ExtensionOperation): List<ExtensionOpStep> = op.steps.filter { it.state != "skipped" }

    /** A step's name; one this app does not know is shown as its id. */
    fun step(res: Resources, id: String, kind: String): String = when (id) {
        "download" -> res.getString(R.string.settings_ext_op_step_download)
        "verify" -> res.getString(R.string.settings_ext_op_step_verify)
        "unpack" -> res.getString(R.string.settings_ext_op_step_unpack)
        "check" -> res.getString(R.string.settings_ext_op_step_check)
        "consent" -> res.getString(R.string.settings_ext_op_step_consent)
        "tools" -> res.getString(R.string.settings_ext_op_step_tools)
        "install" -> res.getString(if (kind == "upgrade") R.string.settings_ext_op_step_upgrade else R.string.settings_ext_op_step_install)
        "start" -> res.getString(R.string.settings_ext_op_step_start)
        "nodes" -> res.getString(R.string.settings_ext_op_step_nodes)
        "stop" -> res.getString(R.string.settings_ext_op_step_stop)
        "remove" -> res.getString(R.string.settings_ext_op_step_remove)
        else -> id
    }

    fun title(res: Resources, op: ExtensionOperation): String = res.getString(when (op.kind) {
        "uninstall" -> R.string.settings_ext_op_title_uninstall
        "upgrade" -> R.string.settings_ext_op_title_upgrade
        else -> R.string.settings_ext_op_title
    })

    /** What a step says beside its name: a download's bytes or that it came from the cache, the computers done. */
    fun detail(res: Resources, s: ExtensionOpStep): String {
        if (s.id == "download") return when {
            s.cached -> res.getString(R.string.settings_ext_op_cached)
            (s.total ?: 0) > 0 && s.state == "running" -> res.getString(R.string.settings_ext_op_bytes_of, size(s.bytes ?: 0), size(s.total!!))
            (s.bytes ?: 0) > 0 -> size(s.bytes!!)
            else -> ""
        }
        // The tools step's bytes are the program downloading: they go on its own line (toolDetail).
        if (s.id == "tools") return ""
        val m = s.machines ?: return ""
        return if (m.total > 0) res.getQuantityString(R.plurals.settings_ext_op_machines, m.total, m.done, m.total) else ""
    }

    /** A program of the tools step: the machine's own copy, or the bytes of the one downloading. */
    fun toolDetail(res: Resources, s: ExtensionOpStep, t: ExtensionOpTool): String = when {
        t.reused -> res.getString(R.string.settings_ext_op_tool_reused)
        t.state == "running" && (s.total ?: 0) > 0 -> res.getString(R.string.settings_ext_op_bytes_of, size(s.bytes ?: 0), size(s.total!!))
        t.state == "running" && (s.bytes ?: 0) > 0 -> size(s.bytes!!)
        else -> ""
    }

    /** The share of the program the tools step downloads now (0–1); null for the others or while its size is not known. */
    fun toolFraction(s: ExtensionOpStep, t: ExtensionOpTool): Float? {
        if (s.state != "running" || t.state != "running" || t.reused) return null
        val total = s.total ?: 0
        return if (total > 0) ((s.bytes ?: 0).toFloat() / total).coerceIn(0f, 1f) else null
    }

    /** Why it failed, in words: the tools step names the program and the reason (`tools_failed`). */
    fun error(res: Resources, op: ExtensionOperation): String? {
        val error = op.error?.takeIf { it.isNotBlank() } ?: return null
        return res.getString(if (op.errorCode == "tools_failed") R.string.settings_ext_op_tools_failed else R.string.settings_ext_op_error, error)
    }

    /** A download's or a sync's fraction (0–1); null when it is not known or the step is not running. */
    fun fraction(s: ExtensionOpStep): Float? {
        if (s.state != "running") return null
        val total = s.total ?: 0
        if (s.id == "download" && total > 0) return ((s.bytes ?: 0).toFloat() / total).coerceIn(0f, 1f)
        if (s.id == "tools") return null
        val m = s.machines ?: return null
        return if (m.total > 0) m.done.toFloat() / m.total else null
    }

    /** The operation in one line: what it is doing now, or how it ended. */
    fun line(res: Resources, op: ExtensionOperation): String = when (op.state) {
        ExtensionOperation.NeedsConsent -> res.getString(R.string.settings_ext_op_needs_consent)
        ExtensionOperation.Succeeded -> done(res, op, op.name ?: op.extension)
        ExtensionOperation.Failed -> res.getString(R.string.settings_ext_op_failed_at, step(res, op.step.orEmpty(), op.kind))
        ExtensionOperation.Canceled -> res.getString(R.string.settings_ext_op_canceled)
        else -> op.steps.firstOrNull { it.id == op.step }?.let { res.getString(R.string.settings_ext_op_running, step(res, it.id, op.kind)) }
            ?: res.getString(R.string.settings_ext_op_starting)
    }

    /** Done of the steps it takes (skipped ones left out). */
    fun progress(op: ExtensionOperation): Pair<Int, Int> = shown(op).let { s -> s.count { it.state == "done" } to s.size }

    /** What the page says once it ended well. */
    fun done(res: Resources, op: ExtensionOperation, name: String): String = when (op.kind) {
        "uninstall" -> res.getString(R.string.settings_ext_uninstalled, name)
        "upgrade" -> res.getString(R.string.settings_ext_upgraded_to, op.version.orEmpty()).trim()
        else -> res.getString(R.string.settings_ext_op_installed, name)
    }

    /** Before the package is installed, the hub can still stop it. */
    fun cancelable(op: ExtensionOperation): Boolean =
        op.state == ExtensionOperation.Running && op.steps.none { it.id in setOf("install", "stop", "remove") && it.state != "pending" }

    /** Whether a is newer than b (the hub's RFC 3339 times; one that does not parse counts as old). */
    fun newer(a: ExtensionOperation, b: ExtensionOperation): Boolean = instant(a.updatedAt) >= instant(b.updatedAt)

    fun instant(iso: String?): Instant = iso?.let { runCatching { Instant.parse(it) }.getOrNull() } ?: Instant.EPOCH

    /**
     * 3.5 MB, 512 KB: one decimal under 10. The unit and the decimals come from the figure as it is shown, after rounding,
     * as the machine pages' `MetricFormat.bytes`: 1023.5 KB and up is 1.0 MB, not 1024 KB; 9.95 KB is 10 KB, not 10.0 KB (#9903).
     */
    fun size(bytes: Long): String {
        if (bytes < 1L shl 10) return "$bytes B"
        val kb = bytes / 1024.0
        return if (Math.round(kb) >= 1024) mb(kb / 1024.0, "MB") else mb(kb, "KB")
    }

    private fun mb(v: Double, unit: String) =
        if (Math.round(v * 10) < 100) String.format(Locale.ROOT, "%.1f %s", v, unit) else String.format(Locale.ROOT, "%.0f %s", v, unit)
}
