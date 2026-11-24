// The module contract (#13430, docs/android/android.md "Modules"): what a `:module:*` project gives the app, and the only
// part of the app a module may name besides the other core modules. It names no module.
plugins {
    id("persona.android.library")
    id("persona.compose")
}

android { namespace = "persona.core.moduleapi" }

dependencies {
    api(project(":core:navigation"))
    api(project(":core:designsystem"))
    api(project(":core:contracts"))
    api(libs.okhttp)
    api(libs.coroutines.core)
    testImplementation(libs.junit)
}
