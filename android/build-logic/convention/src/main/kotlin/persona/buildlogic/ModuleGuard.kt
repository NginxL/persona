package persona.buildlogic

/**
 * Keeps Persona Core free of modules (#13430, docs/android/android.md "Modules"), from the real Gradle project graph:
 * - only `:app` may depend on a `:module:*` project, and in `:app` only `Modules.kt` names a module's code;
 * - a `:core:*` project depends on no `:feature:*`, `:platform:*` or `:app` project.
 * Today's violations sit in a checked-in baseline that can only shrink: a new violation fails, and so does a baseline
 * line that no longer happens (delete it).
 */
object ModuleGuard {
    /** The one file of `:app` that may name a module's code. */
    const val MODULE_LIST = "Modules.kt"
    private val moduleCode = Regex("""\bpersona\.module\.""")

    /** The rule [from] → [to] breaks, or null when it is allowed. */
    fun rule(from: String, to: String): String? = when {
        from == to -> null
        to.startsWith(":module:") && from != ":app" -> "only :app (through Modules.kt) may depend on a module"
        from.startsWith(":core:") && (to.startsWith(":feature:") || to.startsWith(":platform:") || to == ":app") ->
            "a core project depends on no feature, platform or app project"
        else -> null
    }

    /** Violations, one line each (`:core:x -> :module:y`, `:app/src/main/…/Root.kt -> persona.module`), sorted. */
    fun violations(edges: Collection<Pair<String, String>>, appSources: Map<String, String>): List<String> {
        val graph = edges.filter { (from, to) -> rule(from, to) != null }.map { (from, to) -> "$from -> $to" }
        val code = appSources.filter { (path, text) -> !path.endsWith("/$MODULE_LIST") && moduleCode.containsMatchIn(text) }
            .keys.map { "$it -> persona.module" }
        return (graph + code).distinct().sorted()
    }

    /** The baseline's lines: blank lines and `#` comments skipped. */
    fun baseline(text: String): Set<String> =
        text.lines().map(String::trim).filter { it.isNotEmpty() && !it.startsWith("#") }.toSet()

    /** What fails: violations not in the baseline, and baseline lines that no longer happen. Empty: it passes. */
    fun check(violations: List<String>, baseline: Set<String>): List<String> =
        violations.filter { it !in baseline }.map { "new violation: $it" } +
            baseline.filter { it !in violations }.sorted().map { "fixed, delete it from the baseline: $it" }
}
