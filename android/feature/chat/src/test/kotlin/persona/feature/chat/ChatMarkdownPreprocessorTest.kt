package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Test

class ChatMarkdownPreprocessorTest {
    private val done = "Settled **text** with https://example.com and \$x+y\$.\n\n"

    @Test
    fun safeAppendScansOnlyAfterTheSettledBoundary() {
        val scans = mutableListOf<Pair<String, Int>>()
        val preprocessor = ChatMarkdownPreprocessor { text, from -> scans.add(text to from) }

        assertEquals(chatMarkdown(done), preprocessor.rewrite(done))
        val appended = done + "Streaming **tail** with https://example.com/live."
        assertEquals(chatMarkdown(appended), preprocessor.rewrite(appended))
        assertEquals(listOf(done to 0, appended to done.length), scans)
    }

    @Test
    fun protectedContentFallsBackToTheWholeText() {
        listOf("```kotlin\nval x = 1\n```", "[link](https://example.com)", "\$\$\nx+y\n\$\$").forEach { suffix ->
            val scans = mutableListOf<Pair<String, Int>>()
            val preprocessor = ChatMarkdownPreprocessor { text, from -> scans.add(text to from) }
            preprocessor.rewrite(done)

            val text = done + suffix
            assertEquals(suffix, chatMarkdown(text), preprocessor.rewrite(text))
            assertEquals(suffix, listOf(done to 0, text to 0), scans)
        }
    }

    @Test
    fun nonAppendEditRebuildsTheBoundaryForTheNextAppend() {
        val scans = mutableListOf<Pair<String, Int>>()
        val preprocessor = ChatMarkdownPreprocessor { text, from -> scans.add(text to from) }

        assertEquals(chatMarkdown(done), preprocessor.rewrite(done))
        val streaming = done + "Live tail."
        assertEquals(chatMarkdown(streaming), preprocessor.rewrite(streaming))
        val editedDone = done.replace("Settled", "Resettled")
        val edited = editedDone + "Live tail."
        assertEquals(chatMarkdown(edited), preprocessor.rewrite(edited))
        val appended = edited + " More."
        assertEquals(chatMarkdown(appended), preprocessor.rewrite(appended))
        assertEquals(listOf(done to 0, streaming to done.length, edited to 0, appended to editedDone.length), scans)
    }
}
