package persona.module.voice

import android.media.AudioDeviceInfo
import persona.core.designsystem.VoiceOrbMode
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import java.io.IOException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.ext.junit.runners.AndroidJUnit4

/** What the page says, which model a conversation gets, and where its sound goes. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class VoiceTextTest {
    private val text = voiceText()
    private val live = VoiceState(phase = VoiceState.Phase.Live)

    @Test fun theStatusLineSaysWhatIsHappening() {
        assertEquals("正在连接…", statusText(VoiceState(), text))
        assertEquals("在听", statusText(live, text))
        assertEquals("正在说，直接开口就能打断", statusText(live.copy(speaking = true), text))
        assertEquals("已静音", statusText(live.copy(muted = true), text))
        assertEquals("A tool is said over speaking and muting", "查日程…", statusText(live.copy(tool = "查日程", speaking = true, muted = true), text))
        assertEquals("Speaking is said over muted", "正在说，直接开口就能打断", statusText(live.copy(speaking = true, muted = true), text))
        assertEquals("对话已结束", statusText(live.copy(phase = VoiceState.Phase.Ended, tool = "查日程", speaking = true), text))
    }

    @Test fun theOrbFollowsTheSameState() {
        assertEquals(VoiceOrbMode.Connecting, orbMode(VoiceState()))
        assertEquals(VoiceOrbMode.Listening, orbMode(live))
        assertEquals(VoiceOrbMode.Speaking, orbMode(live.copy(speaking = true, muted = true)))
        assertEquals(VoiceOrbMode.Muted, orbMode(live.copy(muted = true)))
        assertEquals(VoiceOrbMode.Ended, orbMode(live.copy(phase = VoiceState.Phase.Ended, speaking = true)))
    }

    @Test fun aVoiceReadsAsGenderLanguagesAndDefault() {
        assertEquals("女声 · 中文、英文 · 默认", voiceDetail(VoiceOption("a", "温柔", "female", listOf("zh", "en")), isDefault = true, text = text))
        assertEquals("男声", voiceDetail(VoiceOption("b", "沉稳", "male", listOf("xx")), isDefault = false, text = text))
        assertEquals("日语", voiceDetail(VoiceOption("c", "元气", null, listOf("ja")), isDefault = false, text = text))
        assertNull(voiceDetail(VoiceOption("d", "无名"), isDefault = false, text = text))
    }

    @Test fun aSocketThatFailsSaysWhy() {
        assertEquals("hub 上没有语音扩展", connectFailure(IOException("Expected HTTP 101"), 404, live = false, text = text))
        assertEquals("Mid-conversation it is a restart or the network", text.restarted, connectFailure(IOException("Broken pipe"), null, live = true, text = text))
        assertEquals("连不上语音服务：连不上 hub", connectFailure(HubFailure(HubError.ConnectionRefused), null, live = false, text = text))
        assertEquals("连不上语音服务：连不上 hub", connectFailure(IOException("unexpected end of stream"), null, live = false, text = text))
        assertEquals("连不上语音服务：语音服务没有响应", connectFailure(HubFailure(HubError.Server, "plugin voice is not answering", 502), 502, live = false, text = text))
        assertEquals("连不上语音服务：这台设备不能用语音对话", connectFailure(HubFailure(HubError.PairingExpired, "forbidden", 403), 403, live = false, text = text))
        assertEquals("连不上语音服务：配对已失效，请重新扫码", connectFailure(HubFailure(HubError.PairingExpired, "unauthorized", 401), 401, live = false, text = text))
        assertEquals("连不上语音服务：HTTP 418", connectFailure(HubFailure(HubError.Server, "teapot", 418), 418, live = false, text = text))
        assertEquals("连不上语音服务：连接失败", connectFailure(HubFailure(HubError.Protocol), null, live = false, text = text))
    }

    @Test fun aCloseSaysWhyOnlyWhenThereIsSomethingToSay() {
        assertEquals(text.restarted, closeMessage(1001, "", live = false, text = text))
        assertNull("The plugin ended it", closeMessage(1000, "bye", live = true, text = text))
        assertEquals("会话超时", closeMessage(4000, " 会话超时 ", live = true, text = text))
        assertEquals(text.restarted, closeMessage(1011, "", live = true, text = text))
        assertNull(closeMessage(1011, "", live = false, text = text))
    }

    @Test fun readFailuresAreToldApartFromNoConfiguration() {
        assertEquals("连不上 hub，连上后点重试。", loadFailure(HubFailure(HubError.Timeout), text))
        assertEquals("hub 没有给出结果：upstream", loadFailure(HubFailure(HubError.Server, "upstream", 502), text))
        assertEquals("hub 没有给出结果。", loadFailure(HubFailure(HubError.Protocol), text))
        assertEquals("配对已失效，请重新扫码", loadFailure(HubFailure(HubError.PairingExpired, "unauthorized", 401), text))
        assertEquals("连不上 hub", voicesFailure(HubFailure(HubError.Unreachable), text))
        assertEquals("no such plugin", voicesFailure(HubFailure(HubError.Server, "no such plugin", 404), text))
        assertEquals("音色列表没拿到：连不上 hub", text.voicesFailed(voicesFailure(IOException("reset"), text)))
    }

    @Test fun pairingAndCompatibilityFailuresResolveWithTheVoicePagesResources() {
        val context = org.robolectric.RuntimeEnvironment.getApplication()
        val expected = mapOf(
            "zh" to "配对已失效，请重新扫码",
            "en" to "Pairing has expired. Scan the code again.",
            "ja" to "ペアリングが無効になりました。もう一度スキャンしてください",
        )
        for ((language, pairing) in expected) {
            val config = android.content.res.Configuration(context.resources.configuration).apply {
                setLocale(java.util.Locale.forLanguageTag(language))
            }
            val resources = context.createConfigurationContext(config).resources
            val words = VoiceText(resources)
            val expired = HubFailure(HubError.PairingExpired, "raw detail", 401)
            assertEquals(language, pairing, loadFailure(expired, words))
            assertEquals(language, pairing, voicesFailure(expired, words))
            assertEquals(language, words.unreachable(pairing),
                connectFailure(expired, 401, live = false, text = words))
            val incompatible = HubFailure(HubError.Incompatible, "raw detail")
            val update = resources.getString(persona.core.designsystem.R.string.designsystem_feedback_incompatible)
            assertEquals(language, update, loadFailure(incompatible, words))
            assertEquals(language, update, voicesFailure(incompatible, words))
            assertEquals(language, words.unreachable(update),
                connectFailure(incompatible, null, live = false, text = words))
        }
    }

    @Test fun theModelComesFromTheModelConfigurationAndIsNeverQuietlySwapped() {
        val models = VoiceModelOptions(listOf(VoiceModelOption("fast", "快"), VoiceModelOption("deep")), default = "deep")
        assertEquals("An older hub: the plugin's default", VoiceSetup.Ready("deep"), resolveVoiceModel(null, models, text))
        assertEquals("No models and no default: none named", VoiceSetup.Ready(null), resolveVoiceModel(null, VoiceModelOptions(), text))
        assertEquals(VoiceSetup.Ready("fast"), resolveVoiceModel(VoiceModelChoice("fast", null), models, text))
        assertEquals("未验证 is usable", VoiceSetup.Ready("fast"), resolveVoiceModel(VoiceModelChoice("fast", "未验证"), models, text))
        assertEquals("Nothing chosen: the plugin's default", VoiceSetup.Ready("deep"), resolveVoiceModel(VoiceModelChoice(null, " "), models, text))
        assertEquals(VoiceSetup.Unavailable("读不到扩展模型列表"), resolveVoiceModel(VoiceModelChoice("fast", "读不到扩展模型列表"), models, text))
        assertEquals(VoiceSetup.Unavailable(text.modelMissing), resolveVoiceModel(VoiceModelChoice("retired", null), models, text))
        assertEquals("A plugin without a catalog has no room for a configured model",
            VoiceSetup.Unavailable(text.modelMissing), resolveVoiceModel(VoiceModelChoice("fast", null), VoiceModelOptions(), text))
    }

    @Test fun soundGoesToAHeadsetElseTheLoudspeakerNeverTheEarpiece() {
        val earpiece = AudioDeviceInfo.TYPE_BUILTIN_EARPIECE
        val speaker = AudioDeviceInfo.TYPE_BUILTIN_SPEAKER
        assertEquals(1, preferredRoute(listOf(earpiece, speaker)))
        assertEquals(2, preferredRoute(listOf(earpiece, speaker, AudioDeviceInfo.TYPE_BLUETOOTH_SCO)))
        assertEquals(0, preferredRoute(listOf(AudioDeviceInfo.TYPE_WIRED_HEADSET, AudioDeviceInfo.TYPE_BLE_HEADSET, speaker)))
        assertEquals(1, preferredRoute(listOf(speaker, AudioDeviceInfo.TYPE_USB_HEADSET)))
        assertNull(preferredRoute(listOf(earpiece)))
        assertNull(preferredRoute(emptyList()))
    }

    @Test fun beforeApi31TheLoudspeakerIsOnUnlessAHeadsetIsConnected() {
        val earpiece = AudioDeviceInfo.TYPE_BUILTIN_EARPIECE
        val speaker = AudioDeviceInfo.TYPE_BUILTIN_SPEAKER
        assertTrue(legacySpeakerphone(listOf(earpiece, speaker)))
        assertFalse(legacySpeakerphone(listOf(earpiece, speaker, AudioDeviceInfo.TYPE_WIRED_HEADSET)))
        assertFalse(legacySpeakerphone(listOf(earpiece, speaker, AudioDeviceInfo.TYPE_USB_HEADSET)))
        assertFalse(legacySpeakerphone(listOf(speaker, AudioDeviceInfo.TYPE_BLUETOOTH_A2DP, AudioDeviceInfo.TYPE_BLUETOOTH_SCO)))
        // Nothing to route to (only the earpiece, or no outputs listed): still the loudspeaker, never the earpiece.
        assertTrue(legacySpeakerphone(listOf(earpiece)))
        assertTrue(legacySpeakerphone(emptyList()))
    }
}
