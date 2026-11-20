import com.android.build.api.variant.ApplicationAndroidComponentsExtension
import com.android.build.api.variant.HasUnitTest
import com.android.build.api.variant.LibraryAndroidComponentsExtension
import persona.buildlogic.CoreTextWords

// Unit tests that expect CoreText in Simplified Chinese (docs/android/android.md "Internationalization"): CoreTextFallback
// reads core/designsystem's strings_core.xml from the test classpath. Only the modules whose tests need it apply this, so
// a change to those strings re-runs their tests and no others; anywhere else CoreText.words stays English (#10712).
val coreTextWords = tasks.register<CoreTextWords>("coreTextWords") {
    words.set(rootProject.layout.projectDirectory.file("core/designsystem/src/main/res/values-zh/strings_core.xml"))
    outputDirectory.set(layout.buildDirectory.dir("generated/coreTextWords"))
}

fun addWords(variant: HasUnitTest) {
    variant.unitTest?.sources?.resources?.addGeneratedSourceDirectory(coreTextWords, CoreTextWords::outputDirectory)
}
pluginManager.withPlugin("com.android.library") {
    extensions.configure<LibraryAndroidComponentsExtension> { onVariants { addWords(it) } }
}
pluginManager.withPlugin("com.android.application") {
    extensions.configure<ApplicationAndroidComponentsExtension> { onVariants { addWords(it) } }
}
