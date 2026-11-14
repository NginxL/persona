package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowDialog
import persona.core.contracts.api.ExtensionDependent
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionPackMember
import persona.core.contracts.api.HubExtension
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** Real pack pages and their actual dialog, in every style, appearance and required language. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
class ExtensionPackSwitchShotsTest {
    @get:Rule val rule = createComposeRule()

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var pageKey by mutableStateOf("on")
    private lateinit var api: PackApi

    private data class PackCopy(
        val enabled: String, val cancel: String, val confirm: String,
        val following: String, val restore: String, val alone: String,
        val runFollowing: String, val runLegacy: String, val title: String, val body: String,
    )

    private data class Scene(val key: String, val detail: ExtensionDetail, val runNote: String, val footer: String)

    private class PackApi(var current: ExtensionDetail) : ExtensionApiStub() {
        val enabledCalls = mutableListOf<Triple<String, Boolean, Boolean>>()
        override suspend fun extension(id: String) = current
        override suspend fun extensions() = listOf(HubExtension("base", "Base"), HubExtension("addon", "Add-on"))
        override suspend fun autoUpgrades() = true
        override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean): ExtensionDetail {
            enabledCalls += Triple(id, enabled, cascade)
            current = current.copy(extension = current.extension.copy(enabled = enabled, status = if (enabled) "running" else "disabled"))
            return current
        }
    }

    private fun copy(locale: String) = if (locale == "zh") PackCopy(
        enabled = "启用", cancel = "取消", confirm = "一起停用",
        following = "停用合集会一起停用它的成员，成员随之停止运行。重新启用合集时，会重新启用当初由合集停用的成员；你自己单独启用或停用过的成员保持你的选择。",
        restore = "重新启用合集会重新启用 Base、Add-on、gone。你自己单独启用或停用过的成员保持你的选择。",
        alone = "停用合集只会停用合集本身。请到每个成员自己的页面启用或停用它。",
        runFollowing = "合集本身不运行程序；成员独立设置和卸载。",
        runLegacy = "合集本身不运行程序；成员独立启用、设置和卸载。",
        title = "停用「Example Pack」和它的成员？",
        body = "它的成员也会一起停用：Base、Add-on。只是停用，不会卸载，设置和数据都会保留。重新启用合集时会重新启用它们。",
    ) else PackCopy(
        enabled = "Enabled", cancel = "Cancel", confirm = "Turn off all",
        following = "Turning the pack off turns off its members too, and they stop running. Turning it back on turns on the members it turned off; a member you turn on or off yourself stays as you set it.",
        restore = "Turning the pack back on turns on Base, Add-on, gone again. A member you turn on or off yourself stays as you set it.",
        alone = "Turning the pack off turns off only the pack itself. Turn each member on or off on its own page.",
        runFollowing = "A pack runs no program itself; its members are set up and removed on their own.",
        runLegacy = "A pack runs no program itself; its members are turned on, set up and removed on their own.",
        title = "Turn off “Example Pack” and its members?",
        body = "Its members are turned off too: Base, Add-on. They're only turned off, not removed; settings and data are kept. Turning the pack back on turns them on again.",
    )

    private fun pack(membersFollow: Boolean, enabled: Boolean, recorded: List<String> = emptyList()) = ExtensionDetail(
        HubExtension("example-pack", "Example Pack", "extension_pack", if (enabled) "running" else "disabled",
            source = "installed", enabled = enabled, version = "1.0.0"),
        extensionPack = listOf(ExtensionPackMember("base"), ExtensionPackMember("addon"), ExtensionPackMember("gone")),
        packMembersFollow = membersFollow,
        packOn = if (membersFollow && enabled) listOf(ExtensionDependent("base", "Base"), ExtensionDependent("addon", "Add-on")) else emptyList(),
        packOff = recorded,
    )

    private fun mount(detail: ExtensionDetail) {
        api = PackApi(detail)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                    key(pageKey) { ExtensionPage(api, "example-pack", onBack = {}) }
                }
            }
        }
    }

    private fun awaitText(text: String) = rule.waitUntil(timeoutMillis = 10_000) {
        rule.onAllNodesWithText(text).fetchSemanticsNodes().isNotEmpty()
    }

    private fun write(name: String, bitmap: Bitmap, light: IntArray?): IntArray {
        assertTrue("capture has dimensions", bitmap.width > 0 && bitmap.height > 0)
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        assertTrue("content rendered", pixels.indices.step(97).map { pixels[it] }.toSet().size > 20)
        if (light != null) assertFalse("light and dark differ", pixels.contentEquals(light))
        File(File("build/extension-pack-switch-shots").also { it.mkdirs() }, name).outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
        return pixels
    }

    private fun capturePage(name: String) {
        var light: IntArray? = null
        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            rule.runOnIdle { style = nextStyle; dark = nextDark }
            rule.waitForIdle()
            if (!nextDark) light = null
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            light = write("$name-${nextStyle.key}-${if (nextDark) "dark" else "light"}.png", bitmap, light)
        }
    }

    private fun captureDialog(name: String, light: IntArray?): IntArray {
        lateinit var bitmap: Bitmap
        rule.runOnIdle {
            val decor = checkNotNull(ShadowDialog.getLatestDialog().window).decorView
            bitmap = Bitmap.createBitmap(decor.width, decor.height, Bitmap.Config.ARGB_8888)
            decor.draw(android.graphics.Canvas(bitmap))
        }
        return write(name, bitmap, light)
    }

    private fun assertOneWholeLine(label: String) {
        val layouts = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText(label).fetchSemanticsNode().config.getOrNull(SemanticsActions.GetTextLayoutResult)?.action?.invoke(layouts)
        assertEquals("$label is laid out once", 1, layouts.size)
        assertEquals("$label stays on one line", 1, layouts.single().lineCount)
        assertFalse("$label is not truncated", layouts.single().isLineEllipsized(0))
    }

    private fun confirmationShots(locale: String, words: PackCopy, large: Boolean) {
        awaitText(words.following)
        var dialogLight: IntArray? = null
        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            rule.runOnIdle { style = nextStyle; dark = nextDark }
            rule.waitForIdle()
            val enabledSwitch = rule.onAllNodes(SemanticsMatcher.keyIsDefined(SemanticsProperties.ToggleableState)
                and SemanticsMatcher.keyIsDefined(SemanticsActions.OnClick))[0].performScrollTo()
            if (large) enabledSwitch.performSemanticsAction(SemanticsActions.OnClick) else enabledSwitch.performClick()
            rule.onNode(isDialog()).assertExists()
            rule.onNodeWithText(words.title).assertExists()
            rule.onNodeWithText(words.body).assertExists()
            assertTrue("nothing is written before confirmation", api.enabledCalls.isEmpty())
            if (large) {
                assertOneWholeLine(words.cancel)
                assertOneWholeLine(words.confirm)
            }
            if (!nextDark) dialogLight = null
            dialogLight = captureDialog("confirm-${nextStyle.key}-${if (nextDark) "dark" else "light"}-$locale${if (large) "-large" else ""}.png", dialogLight)
            rule.onNodeWithText(words.cancel).performClick()
            rule.onNode(isDialog()).assertDoesNotExist()
            assertTrue("cancel changes nothing", api.enabledCalls.isEmpty())
        }
        if (!large) {
            val enabledSwitch = rule.onAllNodes(SemanticsMatcher.keyIsDefined(SemanticsProperties.ToggleableState)
                and SemanticsMatcher.keyIsDefined(SemanticsActions.OnClick))[0].performScrollTo()
            if (large) enabledSwitch.performSemanticsAction(SemanticsActions.OnClick) else enabledSwitch.performClick()
            rule.onNodeWithText(words.confirm).performClick()
            rule.waitUntil { api.enabledCalls.isNotEmpty() }
            assertEquals(listOf(Triple("example-pack", false, false)), api.enabledCalls)
        }
    }

    private fun suite(locale: String) {
        val words = copy(locale)
        val scenes = listOf(
            Scene("on", pack(membersFollow = true, enabled = true), words.runFollowing, words.following),
            Scene("off", pack(membersFollow = true, enabled = false, recorded = listOf("base", "addon", "gone")), words.runFollowing, words.restore),
            Scene("older", pack(membersFollow = false, enabled = true), words.runLegacy, words.alone),
        )
        mount(scenes.first().detail)
        for (scene in scenes) {
            rule.runOnIdle { api.current = scene.detail; pageKey = scene.key }
            awaitText(scene.footer)
            rule.onNodeWithText(scene.runNote).assertExists()
            rule.onNodeWithText(scene.footer).assertExists()
            capturePage("${scene.key}-$locale")
        }
        rule.runOnIdle { api.current = pack(membersFollow = true, enabled = true); pageKey = "confirmation" }
        confirmationShots(locale, words, large = false)
    }

    @Test fun englishPagesAndConfirmation() = suite("en")

    @Config(qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun chinesePagesAndConfirmation() = suite("zh")

    @Config(qualifiers = "en-w400dp-h4000dp-xhdpi", fontScale = 2f)
    @Test fun englishConfirmationAtLargeText() {
        mount(pack(membersFollow = true, enabled = true))
        confirmationShots("en", copy("en"), large = true)
    }

    @Config(qualifiers = "zh-w400dp-h4000dp-xhdpi", fontScale = 2f)
    @Test fun chineseConfirmationAtLargeText() {
        mount(pack(membersFollow = true, enabled = true))
        confirmationShots("zh", copy("zh"), large = true)
    }
}
