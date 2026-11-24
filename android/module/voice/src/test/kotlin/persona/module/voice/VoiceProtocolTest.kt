package persona.module.voice

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** The extension's frames, captions and the start frame (from the old app's VoiceTest), and the pieces around them. */
class VoiceProtocolTest {
    @Test fun parsesTheExtensionsFrames() {
        assertEquals(VoiceEvent.Ready(16000, 24000), VoiceEvent.parse("""{"type":"ready","input_rate":16000,"output_rate":24000,"voice":null}"""))
        assertEquals(VoiceEvent.Clear, VoiceEvent.parse("""{"type":"clear"}"""))
        assertEquals(VoiceEvent.Transcript("u1", true, "你好", false), VoiceEvent.parse("""{"type":"transcript","id":"u1","role":"user","text":"你好","final":false}"""))
        assertEquals(VoiceEvent.Tool("t1", "查知识库", false), VoiceEvent.parse("""{"type":"tool","id":"t1","label":"查知识库","done":false}"""))
        assertEquals(VoiceEvent.Tool("t1", null, true), VoiceEvent.parse("""{"type":"tool","id":"t1","done":true}"""))
        assertEquals(VoiceEvent.Error("boom", true), VoiceEvent.parse("""{"type":"error","message":"boom","fatal":true}"""))
        assertNull(VoiceEvent.parse("""{"type":"something_new"}"""))
        assertNull(VoiceEvent.parse("not json"))
    }

    @Test fun readyRatesAreTheExtensionsButNeverNonsense() {
        assertEquals("Missing rates are the documented defaults", VoiceEvent.Ready(16000, 24000), VoiceEvent.parse("""{"type":"ready"}"""))
        assertEquals(VoiceEvent.Ready(48000, 16000), VoiceEvent.parse("""{"type":"ready","input_rate":48000,"output_rate":16000}"""))
        assertEquals(VoiceEvent.Ready(16000, 24000), VoiceEvent.parse("""{"type":"ready","input_rate":0,"output_rate":-1}"""))
    }

    @Test fun framesWithoutWhatTheyNeedAreSkipped() {
        assertNull("A caption needs its id", VoiceEvent.parse("""{"type":"transcript","role":"user","text":"你好"}"""))
        assertNull("A tool needs its id", VoiceEvent.parse("""{"type":"tool","label":"查日程"}"""))
        assertNull(VoiceEvent.parse("""["ready"]"""))
        assertEquals(VoiceEvent.Transcript("a1", false, "", true), VoiceEvent.parse("""{"type":"transcript","id":"a1","role":"assistant","final":true}"""))
        assertEquals(VoiceEvent.Tool("t2", null, false), VoiceEvent.parse("""{"type":"tool","id":"t2","label":"  "}"""))
        assertEquals("An error without words: the session says something", VoiceEvent.Error(null, false), VoiceEvent.parse("""{"type":"error"}"""))
    }

    @Test fun subtitlesRestateByIdAndAppendNewOnes() {
        var lines = emptyList<Line>()
        lines = lines.with(VoiceEvent.Transcript("u1", true, "今天", false))
        lines = lines.with(VoiceEvent.Transcript("u1", true, "今天有什么会", true))
        lines = lines.with(VoiceEvent.Transcript("a1", false, "我查", false))
        lines = lines.with(VoiceEvent.Transcript("a2", false, "", true)) // an answer cut off before a word: no line
        assertEquals(listOf(Line("u1", true, "今天有什么会", true), Line("a1", false, "我查", false)), lines)
    }

    @Test fun aLineThatEndsEmptyGoesAwayAndOrderStays() {
        var lines = listOf(Line("u1", true, "嗯", false), Line("a1", false, "好的", true))
        lines = lines.with(VoiceEvent.Transcript("u1", true, "", true))
        assertEquals(listOf(Line("a1", false, "好的", true)), lines)
        lines = lines.with(VoiceEvent.Transcript("a1", false, "好的，马上", true))
        assertEquals("Restated in place", listOf(Line("a1", false, "好的，马上", true)), lines)
    }

    @Test fun theStartFrameNamesTheVoiceOnlyWhenChosen() {
        assertEquals("""{"type":"start"}""", startFrame(null))
        assertEquals("""{"type":"start","voice":"zh_x"}""", startFrame("zh_x"))
        assertEquals("""{"type":"start","model":"m1","voice":"zh_x"}""", startFrame("zh_x", "m1"))
        assertEquals("""{"type":"start","model":"m1"}""", startFrame("", "m1"))
        assertEquals("""{"type":"interrupt"}""", InterruptFrame)
    }

    @Test fun theNewestOpenToolIsTheOneSaid() {
        var tools = OpenTools()
        assertNull(tools.current)
        tools = tools.with(VoiceEvent.Tool("kb", "查知识库", false), "处理中")
        tools = tools.with(VoiceEvent.Tool("main", "交给主对话", false), "处理中")
        assertEquals("交给主对话", tools.current)
        tools = tools.with(VoiceEvent.Tool("kb", null, false), "处理中")
        assertEquals("A restated tool keeps its place and its name", "交给主对话", tools.current)
        tools = tools.with(VoiceEvent.Tool("main", null, true), "处理中")
        assertEquals("查知识库", tools.current)
        tools = tools.with(VoiceEvent.Tool("x", null, false), "处理中")
        assertEquals("A tool without a name", "处理中", tools.current)
        tools = tools.with(VoiceEvent.Tool("x", null, true), "处理中").with(VoiceEvent.Tool("kb", null, true), "处理中").with(VoiceEvent.Tool("gone", null, true), "处理中")
        assertNull(tools.current)
    }

    @Test fun theLevelIsTheLoudnessSmoothedForTheOrb() {
        assertEquals(0f, pcmLevel(ShortArray(320), 320), 0f)
        assertEquals(0f, pcmLevel(ShortArray(0), 0), 0f)
        assertEquals(0.5f, pcmLevel(ShortArray(320) { if (it % 2 == 0) 3000 else -3000 }, 320), 0.001f)
        assertEquals("Clipped at full", 1f, pcmLevel(ShortArray(4) { Short.MAX_VALUE }, 4), 0f)
        assertEquals(0.4f, smoothLevel(0f, 1f), 0f)
        assertEquals(0.36f, smoothLevel(0.6f, 0f), 0f)
        var level = 1f
        repeat(20) { level = smoothLevel(level, 0f) }
        assertTrue("Settles at silence", level == 0f)
    }

    @Test fun theModelConfigurationIsTheExtensionsPurpose() {
        // The host opens Settings › Model configuration at this purpose (docs/assistant/model-config.md: extensions are plugin.<id>).
        assertEquals("plugin.voice", voicePurposeId("voice"))
    }
}
