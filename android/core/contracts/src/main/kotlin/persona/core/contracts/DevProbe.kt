package persona.core.contracts

/** A local diagnostics observer. Shared modules call this only; they never see the recorder or its files. */
interface DevProbeSink {
    fun page(name: String)
    fun request(method: String, url: String?, status: Int?, bytesIn: Long, bytesOut: Long, millis: Long)
    fun frame(type: String, bytes: Long)
    fun socket(event: String)
}

/**
 * Process-wide, no-op-by-default diagnostics hooks. Tabs and overlay screens form a stack even while recording is off,
 * so installing a sink starts with the page actually in front. No URL, frame payload or metric is retained here.
 */
object DevProbe {
    private data class Screen(val id: Long, val name: String)
    private val lock = Any()
    private var sink: DevProbeSink? = null
    private var tab: String = "launch"
    private val screens = mutableListOf<Screen>()
    private var nextId = 0L

    fun install(new: DevProbeSink?) {
        val current = synchronized(lock) { sink = new; new to currentPage() }
        current.first?.page(current.second)
    }

    fun tab(name: String) {
        synchronized(lock) { tab = DevScrub.word(name) }
        publish()
    }

    fun enter(name: String): Long {
        val id = synchronized(lock) {
            ++nextId
            screens += Screen(nextId, DevScrub.word(name))
            nextId
        }
        publish()
        return id
    }

    fun leave(id: Long) {
        synchronized(lock) { screens.removeAll { it.id == id } }
        publish()
    }

    fun request(method: String, url: String?, status: Int?, bytesIn: Long, bytesOut: Long, millis: Long) {
        synchronized(lock) { sink }?.request(method, url, status, bytesIn, bytesOut, millis)
    }

    fun frame(type: String, bytes: Long) = synchronized(lock) { sink }?.frame(type, bytes) ?: Unit
    fun frame(record: DevProbeSink.() -> Unit) = synchronized(lock) { sink }?.record() ?: Unit
    fun socket(event: String) = synchronized(lock) { sink }?.socket(event) ?: Unit

    private fun currentPage(): String = screens.lastOrNull()?.name ?: tab

    private fun publish() {
        val current = synchronized(lock) { sink to currentPage() }
        current.first?.page(current.second)
    }

    internal fun resetForTest() = synchronized(lock) {
        sink = null
        tab = "launch"
        screens.clear()
        nextId = 0
    }
}
