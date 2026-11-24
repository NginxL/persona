package persona.buildlogic

import org.gradle.api.Project
import org.jetbrains.kotlin.gradle.dsl.HasConfigurableKotlinCompilerOptions

/**
 * Every Kotlin compilation of the module, main and test sources alike, fails on a dropped must-use result (#13024).
 *
 * `-Xreturn-value-checker=check` reports a call whose result is thrown away when the called declaration is must-use:
 * the standard library's, a declaration under `@MustUseReturnValues`, and everything in a module compiled with
 * `-Xreturn-value-checker=full`. Kotlin reports two cases, both as warnings unless told otherwise: a call standing as a
 * statement (RETURN_VALUE_NOT_USED) and a call ending a lambda whose type returns Unit (RETURN_VALUE_NOT_USED_COERCION),
 * as in `onClick = { decision.accept() }`. Both are errors here, so a feature or app module that drops another module's
 * outcome does not compile, whether it drops it in a statement or in an event lambda. A result dropped on purpose says
 * so where it is dropped: `val _ = call()`.
 *
 * A module in [stillWarnings] keeps the checker on with both cases as warnings, until its sites are fixed.
 *
 * The spellings are Kotlin 2.4.21's (`gradle/libs.versions.toml`). The checker is experimental: a Kotlin update that
 * renames a diagnostic fails every compilation ("warning with name ... does not exist") until this list follows it.
 */
fun Project.configureReturnValueChecks() {
    val errors = if (path in stillWarnings) emptyList() else listOf(
        "-Xwarning-level=RETURN_VALUE_NOT_USED:error",
        "-Xwarning-level=RETURN_VALUE_NOT_USED_COERCION:error",
    )
    extensions.configure(HasConfigurableKotlinCompilerOptions::class.java) {
        compilerOptions.freeCompilerArgs.addAll(listOf("-Xreturn-value-checker=check") + errors)
    }
}

/**
 * The modules where a dropped must-use result is still only a warning, each with the call sites it has left: an open
 * pull request rewrites those lines, so fixing them now would only make that pull request conflict.
 *
 * The list may only shrink. `scripts/test/test_android_return_values.py` fails when a module or a site is added, and
 * when a listed site's line no longer stands as written (a site is a module-relative file and the line that drops the
 * result, indentation aside), however it changed: once the pull request rewrites the line, nothing is left to conflict
 * with. Then fix the result where the line now is and drop its site; a module with no sites left goes too, and from
 * then on it compiles them as errors.
 */
private val stillWarnings: Map<String, List<String>> = mapOf(
    // #11918 rewrites this line (it takes the permission only for a draft); #13128 promotes the module after it.
    ":feature:chat" to listOf(
        "src/main/kotlin/persona/feature/chat/ComposerAttachments.kt: " +
            "runCatching { resolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }",
    ),
)
