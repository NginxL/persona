import persona.buildlogic.configureFilteredTestRuns

plugins {
    id("com.android.application")
}

// A --tests run draws its screenshots instead of taking a result from the build cache (persona/buildlogic/TestRuns.kt).
configureFilteredTestRuns()

val libs = extensions.getByType<VersionCatalogsExtension>().named("libs")
android {
    buildToolsVersion = libs.findVersion("buildTools").get().requiredVersion
    compileSdk = libs.findVersion("compileSdk").get().requiredVersion.toInt()
    defaultConfig {
        minSdk = libs.findVersion("minSdk").get().requiredVersion.toInt()
        targetSdk = libs.findVersion("targetSdk").get().requiredVersion.toInt()
    }
    // CoreText in Simplified Chinese is opt-in per module: persona.coretext.zh.
    compileOptions {
        sourceCompatibility = JavaVersion.toVersion(libs.findVersion("jvm").get().requiredVersion)
        targetCompatibility = JavaVersion.toVersion(libs.findVersion("jvm").get().requiredVersion)
    }
}
