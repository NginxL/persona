package persona.android

import persona.core.moduleapi.ModuleRegistry
import persona.core.moduleapi.PersonaModule

/**
 * The only list of the app's modules (#13430, docs/android/android.md "Modules"), in the order their entries show. The
 * shell (Root.kt, AppServices.kt, the assistant's title menu) reads [Modules.carried] and names no module itself.
 *
 * A module is on when this build carries it (`-Ppersona.modules=<id,id…>`, every module by default; empty builds
 * Persona Core alone) and the paired hub offers its hello feature ([PersonaModule.feature]). A module left out of the
 * build is on the compile classpath only: its entry below is never called, so none of its code loads.
 */
object Modules {
    /** Every module, by id; the `:module:<id>` project of each is in settings.gradle.kts. */
    internal val known: List<Pair<String, () -> PersonaModule>> = listOf(
        module("voice") { persona.module.voice.VoiceModule },
        module("knowledge-ui") { persona.module.knowledge.KnowledgeModule },
    )

    /** The modules this build carries (BuildConfig.PERSONA_MODULES, from `-Ppersona.modules`). */
    val carried: ModuleRegistry by lazy { carrying(BuildConfig.PERSONA_MODULES.toSet()) }

    /**
     * One entry. [create] is typed `() -> PersonaModule` on purpose: a lambda typed with the module's own class would make
     * loading Modules load that class, which a build without the module does not have.
     */
    private fun module(id: String, create: () -> PersonaModule): Pair<String, () -> PersonaModule> = id to create

    /** The registry of the modules in [ids]; unknown ids are a build mistake. */
    internal fun carrying(ids: Set<String>): ModuleRegistry {
        val unknown = ids - known.map { it.first }.toSet()
        require(unknown.isEmpty()) { "Unknown modules $unknown" }
        return ModuleRegistry(known.filter { it.first in ids }.map { it.second() })
    }
}
