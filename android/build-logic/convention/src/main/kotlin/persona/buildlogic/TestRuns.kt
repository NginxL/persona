package persona.buildlogic

import org.gradle.api.Project
import org.gradle.api.tasks.testing.Test

/**
 * A `--tests` run executes its tests: its test tasks neither take a result from the build cache nor store one (#10712).
 *
 * Robolectric screenshot tests write their PNGs to the module's `build/<name>-shots/`, which no task declares, so a
 * result restored from the cache draws nothing and leaves whatever an older run drew. Cache keys match across worktrees,
 * so that result can come from another checkout. A targeted run is how one captures screenshots (and many tests that
 * draw them are not named `*ShotsTest`), so every `--tests` run skips the cache; whole-suite runs, as CI's, keep it.
 * A test task that is UP-TO-DATE ran last time with the same inputs, so its pictures are still the ones it drew.
 */
fun Project.configureFilteredTestRuns() {
    // The requested tasks and their options are part of the configuration cache's key, so reading them here is safe.
    val filtered = gradle.startParameter.taskRequests.any { request ->
        request.args.any { it == "--tests" || it.startsWith("--tests=") }
    }
    if (!filtered) return
    tasks.withType(Test::class.java).configureEach {
        outputs.doNotCacheIf("a --tests run draws screenshots, which the build cache does not hold") { true }
    }
}
