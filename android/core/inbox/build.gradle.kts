// The Inbox's shared types (#13433 item 2, #13847): the repository contract, records, replies, hand-offs and the Lark
// message model that :core:data implements and :feature:inbox draws. No Compose and no page lives here.
plugins {
    id("persona.android.library")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android { namespace = "persona.core.inbox" }

dependencies {
    api(project(":core:contracts"))
    api(project(":core:domain"))
    api(project(":core:hub"))
    implementation(libs.androidx.core)
    api(libs.coroutines.core)
    api(libs.serialization.json)
    implementation("org.jsoup:jsoup:1.18.3")
    testImplementation(libs.junit)
}
