import com.android.build.api.variant.HasUnitTestBuilder
import persona.buildlogic.FileContentProperty
import persona.buildlogic.RelativeDirectoryArgument

plugins {
    id("persona.android.library")
    // Its tests expect CoreText in Simplified Chinese (CoreTextFallback).
    id("persona.coretext.zh")
    alias(libs.plugins.ksp)
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "persona.core.data"
    defaultConfig { testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner" }
}

dependencies {
    implementation(project(":core:contracts"))
    implementation(project(":core:domain"))
    implementation(project(":core:rustcore"))
    implementation(project(":feature:inbox"))
    implementation(libs.androidx.core)
    implementation(libs.room.runtime)
    implementation(libs.room.ktx)
    implementation(libs.datastore)
    api(libs.datastore.preferences)
    implementation(libs.datastore.tink)
    implementation(libs.tink.android)
    implementation(libs.coroutines.android)
    ksp(libs.room.compiler)
    testImplementation(libs.junit)
    testImplementation(libs.coroutines.test)
    testImplementation(project(":core:hub"))
    testImplementation("com.squareup.okhttp3:mockwebserver:${libs.versions.okhttp.get()}")
    // FixtureReader: the shared chat sync scenarios (spec/fixtures/sync, ChatSyncScenariosTest).
    testImplementation(testFixtures(project(":core:domain")))
    // The items cache against a real Room database on the JVM (SessionItemsCacheTest).
    testImplementation("org.robolectric:robolectric:4.17")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
}

// Release unit tests (src/testRelease: no native or shadow class in Release) rerun the whole suite against the release
// variant, so they are off by default; -Ppersona.releaseUnitTests=true turns them on, and scripts/ci/android.sh does.
val releaseUnitTests = providers.gradleProperty("persona.releaseUnitTests").map(String::toBoolean).getOrElse(false)
androidComponents {
    beforeVariants { variant ->
        // The Gradle script accessor sees the builder's base type; AGP's public setter lives on the library variant.
        if (releaseUnitTests && variant is HasUnitTestBuilder && variant.name == "release") {
            variant.javaClass.getMethod("setEnableUnitTest", Boolean::class.javaPrimitiveType).invoke(variant, true)
        }
    }
}

// Room reads and writes its exported schemas here; a relative fingerprint keeps the path out of KSP's cache key (#10712).
ksp { arg(RelativeDirectoryArgument("room.schemaLocation", layout.projectDirectory.dir("schemas").asFile)) }

val continuity = providers.gradleProperty("personaContinuity").orElse("kotlin")
val sortShadow = providers.gradleProperty("persona.sort").orElse("kotlin")
// The host build of the Rust core (:core:rustcore's buildHostCore, for this OS); its contents are the input, not its path.
val hostCore = configurations.create("hostCore") {
    isCanBeConsumed = false
    isCanBeResolved = true
}
dependencies { add(hostCore.name, project(path = ":core:rustcore", configuration = "hostCoreElements")) }
tasks.withType<Test>().configureEach {
    // Adapter parity always exercises JNI; the switch selects the implementation for every repository test.
    jvmArgumentProviders.add(FileContentProperty("persona.core.library", hostCore))
    systemProperty("persona.continuity", continuity.get())
    systemProperty("persona.continuity.frameScheduler", "persona.core.data.FlushContinuityFrameScheduler")
    systemProperty("persona.sort", sortShadow.get())
    if (name == "testDebugUnitTest") forkEvery = 1
}
