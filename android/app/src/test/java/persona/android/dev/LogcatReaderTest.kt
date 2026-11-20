package persona.android.dev

import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.io.OutputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class LogcatReaderTest {
    @Test(timeout = 2_000)
    fun aPipeThatStaysOpenTimesOutInsteadOfBlockingTheCaller() {
        val process = LogcatProcess(exited = false, blocking = true)
        assertEquals(emptyList<String>(), readOwnLogcat(process, timeoutMillis = 50))
        assertTrue("the bounded reader must attempt to drain the pipe", process.readCalls > 0)
    }

    @Test(timeout = 2_000)
    fun timeoutClosesBothInputStreamsAndForciblyReclaimsTheProcess() {
        val process = LogcatProcess(exited = false, blocking = true)
        assertEquals(emptyList<String>(), readOwnLogcat(process, timeoutMillis = 50))
        assertTrue("stdout must be closed", process.stdoutClosed)
        assertTrue("stderr must be closed", process.stderrClosed)
        assertEquals("an unresponsive process must be forcibly reclaimed", 1, process.forcibleCalls)
    }

    @Test fun pipeOutputIsKeptEvenIfTheProcessNeedsToBeReclaimed() {
        val process = LogcatProcess(exited = false)
        assertEquals(listOf("first", "second"), readOwnLogcat(process))
        assertEquals(1, process.forcibleCalls)
    }

    @Test fun completedDumpReturnsItsLines() {
        val process = LogcatProcess(exited = true)
        assertEquals(listOf("first", "second"), readOwnLogcat(process))
        assertTrue("a completed dump must still be read", process.readCalls > 0)
        assertEquals(0, process.forcibleCalls)
    }

    private class LogcatProcess(private val exited: Boolean, blocking: Boolean = false) : Process() {
        var readCalls = 0
        var stdoutClosed = false
        var stderrClosed = false
        var forcibleCalls = 0
        private val release = CountDownLatch(1)
        private val bytes = "first\nsecond\n".toByteArray()
        private val stdout = object : InputStream() {
            private var offset = 0
            override fun read(): Int {
                val next = ByteArray(1)
                return if (read(next, 0, 1) == -1) -1 else next[0].toInt() and 0xff
            }
            override fun read(b: ByteArray, off: Int, len: Int): Int {
                readCalls++
                if (blocking) release.await()
                if (offset == bytes.size) return -1
                val count = minOf(len, bytes.size - offset)
                bytes.copyInto(b, off, offset, offset + count)
                offset += count
                return count
            }
            override fun close() { stdoutClosed = true; release.countDown() }
        }
        private val stderr = object : InputStream() {
            override fun read(): Int = -1
            override fun close() { stderrClosed = true }
        }
        private val stdin = ByteArrayOutputStream()
        private var alive = !exited

        override fun getOutputStream(): OutputStream = stdin
        override fun getInputStream(): InputStream = stdout
        override fun getErrorStream(): InputStream = stderr
        override fun waitFor(): Int { check(exited); return 0 }
        override fun waitFor(timeout: Long, unit: TimeUnit): Boolean = exited
        override fun exitValue(): Int { check(exited); return 0 }
        override fun isAlive(): Boolean = alive
        override fun destroy() = Unit
        override fun destroyForcibly(): Process { forcibleCalls++; alive = false; return this }
    }
}
