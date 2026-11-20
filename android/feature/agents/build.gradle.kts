plugins {
    id("persona.android.library")
    // Its tests expect CoreText in Simplified Chinese (CoreTextFallback).
    id("persona.coretext.zh")
    id("persona.compose")
    id("persona.hilt")
}

android {
    namespace = "persona.feature.agents"
    testOptions {
        unitTests {
            isIncludeAndroidResources = true
            all {
                // RecentSetupsFixtureTest reads spec/fixtures/recents: persona.android.library passes the repository root.
                // The screens' screenshots (the to-dos calendar's day, week and month in three styles, dark and light)
                // share one test process; retain the bounded heap they need rather than the 512 MB default, as feature/settings does.
                it.maxHeapSize = "1g"
            }
        }
    }
}

dependencies {
    implementation(project(":core:contracts"))
    implementation(project(":feature:chat"))
    implementation(project(":core:domain"))
    implementation(project(":core:data"))
    implementation(project(":core:designsystem"))
    implementation(project(":core:navigation"))
    implementation(libs.androidx.lifecycle.compose)
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:${libs.versions.lifecycle.get()}")
    implementation(libs.androidx.activity)
    implementation(libs.datastore.preferences)
    implementation(libs.coroutines.android)
    testImplementation(libs.junit)
    testImplementation(libs.coroutines.test)
    testImplementation(platform(libs.compose.bom))
    testImplementation("androidx.compose.ui:ui-test-junit4")
    testImplementation("androidx.compose.ui:ui-test-manifest")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test.ext:junit:1.3.0")
    testImplementation(project(":core:hub"))
    testImplementation(project(":core:data"))
}
