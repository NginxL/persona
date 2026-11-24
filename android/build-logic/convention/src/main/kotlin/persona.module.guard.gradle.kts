// The module guard (docs/android/android.md "Modules"), applied to :app: `./gradlew :app:moduleGuard`. It reads every
// project of the build, not only the app's.
import persona.buildlogic.ModuleGuardTask

tasks.register<ModuleGuardTask>("moduleGuard") {
    group = "verification"
    description = "Fails when a core project depends on a module, or on a feature, platform or the app, beyond the baseline."
    // Every project is configured before the task graph is built; only strings reach the task (configuration cache).
    val graph = rootProject.subprojects.flatMap { project ->
        project.configurations.flatMap { configuration ->
            configuration.dependencies.withType(ProjectDependency::class.java).map { "${project.path} -> ${it.path}" }
        }
    }.distinct().sorted()
    edges.set(graph)
    root.set(rootProject.layout.projectDirectory)
    appSources.from(fileTree("src") { include("**/*.kt", "**/*.java") })
    baseline.set(rootProject.layout.projectDirectory.file("module-guard-baseline.txt"))
    report.set(layout.buildDirectory.file("reports/module-guard.txt"))
}
