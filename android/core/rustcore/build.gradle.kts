// The shared Rust core's Android binding (docs/plans/plan-rust-core.md): the JNI bridge, a typed wrapper and the codecs
// persona-core-typegen generates from the core's wire types. Unit tests load a host build of the same library.
import persona.buildlogic.FileContentProperty
import persona.buildlogic.HostCoreLibrary
import persona.buildlogic.hostLibraryName

plugins {
    id("persona.jvm.library")
}

// Answer shadow declarations are packaged separately from rustcore.jar, so Release does not carry the wrapper.
val shadowSource = sourceSets.create("shadow") {
    java.srcDir("src/shadow/kotlin")
    compileClasspath += sourceSets.main.get().output
    runtimeClasspath += sourceSets.main.get().output
}
configurations.named(shadowSource.compileClasspathConfigurationName) {
    extendsFrom(configurations.implementation.get())
}
configurations.named(shadowSource.runtimeClasspathConfigurationName) {
    extendsFrom(configurations.runtimeOnly.get())
}
// friendPaths, not an -Xfriend-paths= argument: the argument's absolute path would be part of the cache key (#10712).
tasks.named("compileShadowKotlin", org.jetbrains.kotlin.gradle.tasks.KotlinJvmCompile::class.java).configure {
    friendPaths.from(sourceSets.main.get().output.classesDirs)
}
val shadowRuntimeElements = configurations.create("shadowRuntimeElements") {
    isCanBeConsumed = true
    isCanBeResolved = false
    attributes.attribute(
        org.gradle.api.attributes.Usage.USAGE_ATTRIBUTE,
        objects.named(org.gradle.api.attributes.Usage::class.java, org.gradle.api.attributes.Usage.JAVA_RUNTIME),
    )
}
val shadowJar = tasks.register<Jar>("shadowJar") {
    archiveBaseName.set("rustcore-shadow")
    from(shadowSource.output)
    dependsOn(shadowSource.classesTaskName)
}
artifacts.add(shadowRuntimeElements.name, shadowJar.flatMap { it.archiveFile }) {
    builtBy(shadowJar)
}

dependencies {
    testImplementation(testFixtures(project(":core:domain")))
}

val coreWorkspace = rootProject.layout.projectDirectory.dir("../native/app-core")
// Everything cargo reads to build persona-core-ffi: a change to any of them rebuilds the library, nothing else does.
val coreSources = files(
    coreWorkspace.file("Cargo.toml"),
    coreWorkspace.file("Cargo.lock"),
    coreWorkspace.file("rust-toolchain.toml"),
    fileTree(coreWorkspace.dir("crates")) { include("**/*.rs", "**/Cargo.toml", "**/*.h") },
)
tasks.withType<HostCoreLibrary>().configureEach {
    sources.from(coreSources)
    features.set(listOf("test-support"))
    host.set(providers.systemProperty("os.name").zip(providers.systemProperty("os.arch")) { os, arch -> "$os/$arch" })
    for (name in listOf("RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS")) {
        rustFlags.put(name, providers.environmentVariable(name).orElse(""))
    }
    workspace.set(coreWorkspace)
}

// Needs rustup; the workspace's rust-toolchain.toml picks the compiler. The tests load the copy in this build directory.
val buildHostCore = tasks.register<HostCoreLibrary>("buildHostCore") {
    description = "Builds the host JNI library (dev profile) that the host-JVM tests load."
    profile.set("dev")
    cargoTargetDirectory.set(coreWorkspace.dir("target"))
    library.set(layout.buildDirectory.file("native/host/" + hostLibraryName()))
}
// core:domain's and core:data's tests load the same library through this configuration.
val hostCoreElements = configurations.create("hostCoreElements") {
    isCanBeConsumed = true
    isCanBeResolved = false
}
artifacts.add(hostCoreElements.name, buildHostCore.flatMap { it.library })

// Linux-only host diagnostic; keep the measurement release target separate from normal release builds.
val buildReleaseHostCore = tasks.register<HostCoreLibrary>("buildReleaseHostCore") {
    description = "Builds the release-profile host JNI library for boundaryCostTest."
    profile.set("release")
    cargoTargetDirectory.set(coreWorkspace.dir("target/boundary-measurement-target"))
    library.set(layout.buildDirectory.file("native/host-release/" + hostLibraryName()))
}
val releaseHostLibrary = files(buildReleaseHostCore.flatMap { it.library })
// Ordinary tests exclude the opt-in measurement classes; this task always produces their raw reports.
val boundaryCostTest = tasks.register<Test>("boundaryCostTest") {
    group = "verification"
    description = "Host-only segmented Runtime/JNI/codec reference; not a device acceptance benchmark."
    testClassesDirs = sourceSets.test.get().output.classesDirs
    classpath = sourceSets.test.get().runtimeClasspath
    jvmArgumentProviders.add(FileContentProperty("persona.core.library", releaseHostLibrary))
    jvmArgumentProviders.add(FileContentProperty("persona.core.boundary.library", releaseHostLibrary))
    environment("PERSONA_MEASUREMENT_OUTPUT", layout.buildDirectory.dir("reports/boundary-cost").get().asFile.absolutePath)
    environment("PERSONA_CODEC_COMPARISON_OUTPUT", layout.buildDirectory.dir("reports/codec-comparison").get().asFile.absolutePath)
    environment("PERSONA_MEASUREMENT_NONCE", providers.gradleProperty("personaBoundaryNonce").orElse("boundary-cost-default").get())
    // A measurement: it runs every time it is asked for, and it is never part of test.
    outputs.upToDateWhen { false }
    filter { includeTestsMatching("persona.core.rustcore.RustCoreBoundaryCostTest") }
    filter { includeTestsMatching("persona.core.rustcore.KotlinCodecComparisonTest") }
}

tasks.withType<Test>().configureEach {
    if (name != "boundaryCostTest") {
        // The library's contents, not its path, decide whether the tests are up to date (docs/lessons/testing.md).
        jvmArgumentProviders.add(FileContentProperty("persona.core.library", files(buildHostCore.flatMap { it.library })))
    }
    if (name == "test") {
        filter {
            excludeTestsMatching("persona.core.rustcore.RustCoreBoundaryCostTest")
            excludeTestsMatching("persona.core.rustcore.KotlinCodecComparisonTest")
        }
    }
}
