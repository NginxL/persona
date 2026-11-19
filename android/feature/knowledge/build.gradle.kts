plugins {
    id("persona.android.library")
    // Its tests expect CoreText in Simplified Chinese (CoreTextFallback).
    id("persona.coretext.zh")
    id("persona.compose")
    id("persona.hilt")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "persona.feature.knowledge"
    // The pages are drawn under Robolectric in each language (KnowledgeShotsTest).
    testOptions { unitTests { isIncludeAndroidResources = true } }
}

dependencies {
    implementation(project(":core:contracts"))
    implementation(project(":core:designsystem"))
    implementation(project(":core:navigation"))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.androidx.lifecycle.compose)
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:${libs.versions.lifecycle.get()}")
    implementation(libs.androidx.navigation3.runtime)
    implementation(libs.coroutines.android)
    implementation(libs.serialization.json)
    implementation(libs.markdown)
    testImplementation(libs.junit)
    testImplementation(libs.coroutines.test)
    testImplementation(platform(libs.compose.bom))
    testImplementation("androidx.compose.ui:ui-test-junit4")
    testImplementation("androidx.compose.ui:ui-test-manifest")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test.ext:junit:1.3.0")
}
