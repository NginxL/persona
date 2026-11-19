plugins {
    id("persona.android.library")
    // Its tests expect CoreText in Simplified Chinese (CoreTextFallback).
    id("persona.coretext.zh")
    id("persona.compose")
    id("persona.hilt")
}

android {
    namespace = "persona.feature.chat"
    // A message's source line is drawn on the JVM (Robolectric's native graphics) for its screenshots.
    testOptions {
        unitTests {
            isIncludeAndroidResources = true
            all {
                // The screenshot classes share one test process; full GCs left 498 MiB live on the 512 MB default,
                // led by Robolectric resource/theme tables. Leave room for more captures, as the other screenshot modules do.
                it.maxHeapSize = "1g"
            }
        }
    }
    defaultConfig { testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner" }
}

dependencies {
    implementation(project(":core:contracts"))
    implementation(project(":core:domain"))
    implementation(project(":core:designsystem"))
    implementation(project(":core:chatlist"))
    implementation(project(":core:navigation"))
    implementation(project(":feature:inbox"))
    implementation(libs.androidx.activity)
    implementation(libs.androidx.webkit)
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.androidx.lifecycle.compose)
    implementation(libs.androidx.core)
    implementation(libs.androidx.navigation3.runtime)
    implementation(libs.coroutines.android)
    implementation(libs.serialization.json)
    implementation(libs.markdown)
    implementation(libs.coil.compose)
    testImplementation(libs.junit)
    testImplementation(libs.coroutines.test)
    testImplementation(platform(libs.compose.bom))
    testImplementation("androidx.compose.ui:ui-test-junit4")
    testImplementation("androidx.compose.ui:ui-test-manifest")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
}
