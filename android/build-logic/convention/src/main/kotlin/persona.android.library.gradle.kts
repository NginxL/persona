import persona.buildlogic.configureFilteredTestRuns
import persona.buildlogic.configureRepositoryTests

plugins {
    id("com.android.library")
}

// The repository root and the shared fixture domains this module's tests read, as persona.jvm.library sets them.
configureRepositoryTests()
// A --tests run draws its screenshots instead of taking a result from the build cache (persona/buildlogic/TestRuns.kt).
configureFilteredTestRuns()

val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")
android {
    buildToolsVersion = libs.findVersion("buildTools").get().requiredVersion
    compileSdk = libs.findVersion("compileSdk").get().requiredVersion.toInt()
    defaultConfig {
        minSdk = libs.findVersion("minSdk").get().requiredVersion.toInt()
    }
    // Robolectric tests run in Simplified Chinese unless they say otherwise (build-logic/robolectric).
    sourceSets.getByName("test").resources.srcDir(rootProject.file("build-logic/robolectric"))
    // CoreText in Simplified Chinese is opt-in per module: persona.coretext.zh.
    testOptions.unitTests.all { test ->
        // Robolectric under JDK 21 accesses raw FileDescriptor internals and the internal access bridge.
        test.jvmArgs(
            "--add-opens=java.base/java.io=ALL-UNNAMED",
            "--add-exports=java.base/jdk.internal.access=ALL-UNNAMED"
        )
    }
    compileOptions {
        sourceCompatibility = JavaVersion.toVersion(libs.findVersion("jvm").get().requiredVersion)
        targetCompatibility = JavaVersion.toVersion(libs.findVersion("jvm").get().requiredVersion)
    }
}
