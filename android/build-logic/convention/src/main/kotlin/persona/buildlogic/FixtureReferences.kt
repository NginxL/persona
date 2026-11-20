package persona.buildlogic

import java.io.File
import org.gradle.api.DefaultTask
import org.gradle.api.GradleException
import org.gradle.api.Project
import org.gradle.api.Transformer
import org.gradle.api.file.ConfigurableFileCollection
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.file.RegularFile
import org.gradle.api.file.RegularFileProperty
import org.gradle.api.tasks.CacheableTask
import org.gradle.api.tasks.IgnoreEmptyDirectories
import org.gradle.api.tasks.InputFiles
import org.gradle.api.tasks.Internal
import org.gradle.api.tasks.OutputFile
import org.gradle.api.tasks.PathSensitive
import org.gradle.api.tasks.PathSensitivity
import org.gradle.api.tasks.TaskAction
import org.gradle.api.tasks.testing.Test

/**
 * Lists the shared fixture domains (`spec/fixtures/<domain>`) a module's tests read, so its test tasks declare only those
 * as inputs: a change to one domain re-runs the modules that read it and no others (#10712).
 *
 * Tests name a domain in one of two ways, and the scan trusts nothing else:
 * - a path that starts with it, `spec/fixtures/<domain>`, in code or in a comment;
 * - `FixtureReader.read("<domain>")`.
 *
 * A fixture path built from a variable would hide its domain, and a test reading it could pass from the cache after the
 * fixture changed. Such a line fails this task unless it explains, in a `fixture-inputs:` comment, where its callers name
 * the domain (FixtureReader's own path does). A path whose domain is completed by a variable (`spec/fixtures/todo-$kind`)
 * counts as one; a named domain that does not exist under spec/fixtures fails too, which also catches the partial name
 * such a path leaves when the scan does not recognise it, and plain typos. The scan runs at execution time, so editing a
 * test never invalidates the configuration cache.
 */
@CacheableTask
abstract class FixtureReferences : DefaultTask() {
    @get:InputFiles @get:PathSensitive(PathSensitivity.RELATIVE) @get:IgnoreEmptyDirectories
    abstract val sources: ConfigurableFileCollection

    /**
     * spec/fixtures, to check that each named domain exists. Not an input: a list restored from the cache was written
     * where every domain it names existed, and a domain missing here is fingerprinted as missing by the test task, which
     * then runs rather than coming from the cache.
     */
    @get:Internal abstract val fixtures: DirectoryProperty

    @get:OutputFile abstract val domains: RegularFileProperty

    @TaskAction fun scan() {
        val found = sortedSetOf<String>()
        val problems = mutableListOf<String>()
        val missing = mutableListOf<String>()
        val root = fixtures.get().asFile
        sources.asFileTree.visit {
            if (isDirectory) return@visit
            val text = file.readText()
            val named = domainsIn(text)
            found += named
            unresolvedLines(text).forEach { line -> problems += "${relativePath.pathString}:$line" }
            named.filterNot { File(root, it).exists() }.mapTo(missing) { domain -> "${relativePath.pathString}: $domain" }
        }
        if (problems.isNotEmpty()) {
            throw GradleException(
                "A test builds a spec/fixtures path from a variable, so its fixtures cannot be declared as inputs:\n" +
                    problems.joinToString("\n") { "  $it" } +
                    "\nName the domain as spec/fixtures/<domain> or FixtureReader.read(\"<domain>\"), or add a " +
                    "`$MARKER <where the callers name it>` comment to that line (persona/buildlogic/FixtureReferences.kt).",
            )
        }
        if (missing.isNotEmpty()) {
            throw GradleException(
                "A test names a spec/fixtures domain that does not exist, so the fixtures it reads are not inputs:\n" +
                    missing.joinToString("\n") { "  $it" } +
                    "\nWrite the whole domain name (a path completed by a variable hides it), or fix the name " +
                    "(persona/buildlogic/FixtureReferences.kt).",
            )
        }
        domains.get().asFile.writeText(found.joinToString("") { "$it\n" })
    }

    companion object {
        const val MARKER = "fixture-inputs:"
        private const val NAME = "[A-Za-z0-9_][A-Za-z0-9_.-]*"
        private val path = Regex("""spec/fixtures/($NAME)""")
        private val reader = Regex("""FixtureReader\.read\(\s*"($NAME)"""")
        // The root alone, an interpolated or concatenated domain, a domain name that a variable completes
        // ("spec/fixtures/todo-$kind", "spec/fixtures/todo-" + kind), a reader call whose argument is not a plain
        // literal, or a path put together from separate segments.
        private val dynamic = Regex(
            """spec/fixtures(?:"|/[$"{])|spec/fixtures/$NAME(?:\$|"\s*\+)""" +
                """|FixtureReader\.read\(\s*(?:[^"\s)]|"[^"]*\$)|"fixtures"""",
        )

        /** The domain names (a directory, or a file directly under spec/fixtures) this source names. */
        fun domainsIn(text: String): Set<String> =
            (path.findAll(text).map { it.groupValues[1].trimEnd('.') } + reader.findAll(text).map { it.groupValues[1] })
                .filter { it.isNotEmpty() }
                .toSortedSet()

        /** One-based numbers of the lines that build a fixture path the scan cannot read, without a [MARKER]. */
        fun unresolvedLines(text: String): List<Int> =
            text.lines().withIndex().filter { (_, line) -> dynamic.containsMatchIn(line) && MARKER !in line }.map { it.index + 1 }
    }
}

/** The paths under spec/fixtures that a [FixtureReferences] list names. */
class FixtureDirectories(private val fixtures: File) : Transformer<List<File>, RegularFile> {
    override fun transform(list: RegularFile): List<File> =
        list.asFile.readLines().filter { it.isNotBlank() }.map { File(fixtures, it) }
}

/**
 * What a library's unit tests share: the repository root as `persona.repo.root` (a location, not an input), and as
 * inputs only the spec/fixtures domains the module's test sources name ([FixtureReferences]).
 */
fun Project.configureRepositoryTests() {
    val repositoryRoot = providers.gradleProperty("personaRepoRoot").map { File(it) }
        .getOrElse(rootProject.layout.projectDirectory.dir("..").asFile).toPath().normalize().toFile()
    val references = tasks.register("fixtureReferences", FixtureReferences::class.java) {
        description = "Lists the spec/fixtures domains this module's tests read."
        // Every source set but production code and device tests (which read APK assets, not the repository), so a new
        // host-test source set (testDebug, testShadow, sharedTest, testFixtures…) is scanned without being listed here.
        sources.from(fileTree("src") {
            include("**/*.kt", "**/*.java")
            exclude("main/", "debug/", "release/", "androidTest*/")
        })
        fixtures.set(File(repositoryRoot, "spec/fixtures"))
        domains.set(layout.buildDirectory.file("fixture-references.txt"))
    }
    val fixtures = references.flatMap { it.domains }.map(FixtureDirectories(File(repositoryRoot, "spec/fixtures")))
    tasks.withType(Test::class.java).configureEach {
        jvmArgumentProviders.add(LocationProperty("persona.repo.root", repositoryRoot))
        inputs.files(fixtures).withPropertyName("sharedFixtures").withPathSensitivity(PathSensitivity.RELATIVE)
    }
}
