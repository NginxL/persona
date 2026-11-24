pluginManagement {
    includeBuild("build-logic")
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("androidx\\..*")
                includeGroup("com.google.testing.platform")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("androidx\\..*")
                includeGroup("com.google.testing.platform")
            }
        }
        mavenCentral()
    }
}
rootProject.name = "Persona"
include(":core:demo")
include(":feature:machines")
include(":feature:files")
include(":app", ":core:contracts", ":core:domain", ":core:terminal", ":core:rustcore", ":core:rustcore-android")

include(
    ":core:navigation", ":core:hub", ":core:data", ":core:designsystem", ":core:chatlist",
    ":feature:chat", ":feature:agents", ":feature:assistant", ":feature:inbox", ":feature:settings",
    ":platform:notify",
)
include(":feature:terminal")
include(":platform:phonetools")
include(":platform:shortcuts")
include(":core:moduleapi")
include(":core:inbox")
include(":platform:capsule")
// Modules (docs/android/android.md "Modules"): features outside Persona Core. The app's Modules.kt lists them; no core
// module may depend on one (scripts/app/android_module_guard.py).
include(":module:voice")
include(":module:knowledge-ui")
