// The shared Rust core's Android binding (docs/plans/plan-rust-core.md): the JNI bridge, a typed wrapper and the codecs
// persona-core-typegen generates from the core's wire types. Unit tests load a host build of the same library.
plugins {
    id("persona.jvm.library")
}

dependencies {
    testImplementation(testFixtures(project(":core:domain")))
}

val coreWorkspace = rootProject.layout.projectDirectory.dir("../native/app-core")
val hostLibrary = coreWorkspace.file(
    "target/debug/" + when {
        org.gradle.internal.os.OperatingSystem.current().isMacOsX -> "libpersona_core.dylib"
        org.gradle.internal.os.OperatingSystem.current().isWindows -> "persona_core.dll"
        else -> "libpersona_core.so"
    }
)

// Cargo is incremental, so this runs every time and costs little when nothing changed. Needs rustup; the workspace's
// rust-toolchain.toml picks the compiler.
val buildHostCore by tasks.registering(Exec::class) {
    workingDir(coreWorkspace)
    commandLine("cargo", "build", "--locked", "-p", "persona-core-ffi")
    outputs.file(hostLibrary)
    outputs.upToDateWhen { false }
}

tasks.withType<Test>().configureEach {
    // The library's contents, not just its path, decide whether the tests are up to date.
    inputs.files(buildHostCore).withPropertyName("hostCore").withPathSensitivity(PathSensitivity.NONE)
    systemProperty("persona.core.library", hostLibrary.asFile.absolutePath)
}
