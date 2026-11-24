package persona.buildlogic

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ModuleGuardTest {
    @Test fun aCoreOrFeatureProjectOnAModuleFails() {
        for (from in listOf(":core:data", ":core:moduleapi", ":feature:assistant", ":platform:notify", ":module:other")) {
            val violations = ModuleGuard.violations(listOf(from to ":module:voice"), emptyMap())
            assertEquals(listOf("$from -> :module:voice"), violations)
            assertEquals(listOf("new violation: $from -> :module:voice"), ModuleGuard.check(violations, emptySet()))
        }
    }

    @Test fun theAppMayDependOnAModuleButNameItsCodeOnlyInModulesKt() {
        assertNull(ModuleGuard.rule(":app", ":module:voice"))
        val sources = mapOf(
            ":app/src/main/java/persona/android/Modules.kt" to "import persona.module.voice.VoiceModule",
            ":app/src/main/java/persona/android/ui/Root.kt" to "persona.module.voice.rememberVoiceExtension()",
            ":app/src/main/java/persona/android/ui/Shell.kt" to "import persona.core.moduleapi.PersonaModule",
        )
        assertEquals(listOf(":app/src/main/java/persona/android/ui/Root.kt -> persona.module"),
            ModuleGuard.violations(listOf(":app" to ":module:voice"), sources))
    }

    @Test fun coreStaysBelowFeaturesPlatformAndTheApp() {
        assertEquals(listOf(":core:data -> :app", ":core:data -> :feature:inbox", ":core:data -> :platform:notify"),
            ModuleGuard.violations(listOf(":core:data" to ":feature:inbox", ":core:data" to ":platform:notify",
                ":core:data" to ":app", ":core:data" to ":core:hub", ":feature:chat" to ":feature:files",
                ":module:voice" to ":core:moduleapi"), emptyMap()))
    }

    @Test fun theBaselineOnlyShrinks() {
        val baseline = ModuleGuard.baseline("# known\n\n:core:data -> :feature:inbox\n")
        assertEquals(setOf(":core:data -> :feature:inbox"), baseline)
        assertTrue(ModuleGuard.check(listOf(":core:data -> :feature:inbox"), baseline).isEmpty())
        // Fixed: the line has to go, so it cannot come back unnoticed.
        assertEquals(listOf("fixed, delete it from the baseline: :core:data -> :feature:inbox"), ModuleGuard.check(emptyList(), baseline))
    }
}
