package persona.core.moduleapi

/**
 * The modules a build carries, in the order their entries show (the app's `Modules.kt` builds it). A module is active on
 * a hub unless that hub publishes module flags ([MODULES_MARKER]) and lacks the module's [PersonaModule.feature]
 * ([moduleFeature]); everything else is absent. Every
 * carried module gets its [PersonaModule.services] for a pairing, active or not, since a hub can start offering a feature
 * later; its pages and actions come only while it is active, and the shell shows its unavailable page for a module page
 * left open when the module turns inactive. Contributions the Android shell does not serve yet are reported to [warn].
 */
class ModuleRegistry(val modules: List<PersonaModule>, warn: (String) -> Unit = ::logWarning) {
    init {
        modules.forEach { require(it.id.matches(ModuleId)) { "Module id '${it.id}' is not lower-case and hyphenated" } }
        val repeated = modules.groupBy { it.id }.filterValues { it.size > 1 }.keys
        require(repeated.isEmpty()) { "Modules listed twice: $repeated" }
        // Declared but not yet served on Android (docs/android/android.md "Modules"): said loudly, never dropped quietly.
        modules.forEach { module ->
            if (module.audioUses.isNotEmpty())
                warn("Module '${module.id}' declares audio uses, but the shell has no audio broker yet; nothing arbitrates them")
        }
    }

    /**
     * The modules active on a hub whose hello offers [features] (the rule every client follows, #13844): a module with a
     * feature is off only when the hello says the hub publishes module flags ([MODULES_MARKER]) and lacks the module's
     * flag. No hello yet, demo mode (both an empty set) and an older hub without the marker leave every module on, so
     * defaults do not change.
     */
    fun active(features: Set<String>): List<PersonaModule> = modules.filter { isActive(it, features) }

    /** The Settings pages of the modules active on a hub whose hello offers [features], in list order. */
    fun settingsPages(features: Set<String>): List<ModuleSettingsPage> = active(features).flatMap { it.settingsPages() }

    /** Each module's [PersonaModule.services] for one pairing, by id; modules that keep none are left out. */
    fun services(hub: ModuleHub): Map<String, ModuleServices> =
        modules.mapNotNull { module -> module.services(hub)?.let { module.id to it } }.toMap()

    /** The debug pages of every module ([PersonaModule.debugPages]). */
    fun debugPages() = modules.flatMap { it.debugPages().entries }.associate { it.key to it.value }

    companion object {
        /** Android's log, or standard error where there is none (JVM tests). */
        fun logWarning(message: String) {
            try { android.util.Log.w("PersonaModules", message) } catch (_: RuntimeException) { System.err.println("PersonaModules: $message") }
        }

        /** The hello feature a hub announces when it publishes module flags (#13844). */
        const val MODULES_MARKER = "modules"

        /** The hello feature that turns module [id] on, `module.<id>`: what a module's [PersonaModule.feature] names. */
        fun moduleFeature(id: String): String = "module.$id"

        /** Whether [module] is on for a hub whose hello offers [features] ([active]). */
        fun isActive(module: PersonaModule, features: Set<String>): Boolean {
            val feature = module.feature ?: return true
            return MODULES_MARKER !in features || feature in features
        }

        val Empty = ModuleRegistry(emptyList())
        private val ModuleId = Regex("[a-z][a-z0-9]*(-[a-z0-9]+)*")
    }
}
