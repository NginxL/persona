plugins {
    `kotlin-dsl`
}

kotlin {
    jvmToolchain(libs.versions.jdk.get().toInt())
}

dependencies {
    implementation(libs.android.gradlePlugin)
    implementation(libs.kotlin.gradlePlugin)
    implementation(libs.kotlin.composePlugin)
    implementation(libs.kotlin.serializationPlugin)
    implementation(libs.hilt.gradlePlugin)
    implementation(libs.ksp.gradlePlugin)
}

// ModuleGuard's own tests (scripts/ci/android.sh runs them with ./gradlew -p build-logic :convention:test).
dependencies {
    testImplementation(libs.junit)
}
