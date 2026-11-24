import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import persona.buildlogic.configureFilteredTestRuns
import persona.buildlogic.configureRepositoryTests
import persona.buildlogic.configureReturnValueChecks

plugins {
    id("org.jetbrains.kotlin.jvm")
    `java-library`
}

val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")
kotlin {
    jvmToolchain(libs.findVersion("jdk").get().requiredVersion.toInt())
    compilerOptions {
        jvmTarget.set(JvmTarget.fromTarget(libs.findVersion("jvm").get().requiredVersion))
    }
}
java {
    sourceCompatibility = JavaVersion.toVersion(libs.findVersion("jvm").get().requiredVersion)
    targetCompatibility = sourceCompatibility
}
dependencies {
    "testImplementation"(libs.findLibrary("junit").get())
}
// The repository root and the shared fixture domains this module's tests read (persona/buildlogic/FixtureReferences.kt).
configureRepositoryTests()
// A --tests run executes instead of taking a result from the build cache (persona/buildlogic/TestRuns.kt).
configureFilteredTestRuns()
// A dropped must-use result is a compile error (persona/buildlogic/ReturnValues.kt).
configureReturnValueChecks()
tasks.withType<Test>().configureEach {
    useJUnit()
    testLogging {
        events("failed", "skipped")
        showStandardStreams = true
    }
}
