package persona.android.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The home's Persona button carries a still Needs You dot while something waits at another assistant, as iOS's
 * `AssistantWaitingDot`, not the breathing one: a breath there redrew the whole window every frame while the page sat
 * still. The dot is drawn deep inside [Root]'s home chrome, so this reads the call where it is made.
 */
class OthersWaitingDotTest {
    @Test fun theOthersWaitingDotIsStill() {
        val lines = source().lines()
        val calls = lines.indices.filter { "currentOthersWaiting.value) {" in lines[it] }
            .map { lines.subList(it, (it + 3).coerceAtMost(lines.size)).joinToString("\n") }
        assertEquals(calls.joinToString("\n"), 1, calls.size)
        assertTrue(calls.single(), "breathes = false" in calls.single())
    }

    private fun source(): String = generateSequence(File(System.getProperty("user.dir"))) { it.parentFile }
        .map { File(it, "android/app/src/main/java/persona/android/ui/Root.kt") }.first { it.isFile }.readText()
}
