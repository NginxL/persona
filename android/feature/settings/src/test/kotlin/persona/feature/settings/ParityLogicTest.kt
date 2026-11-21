package persona.feature.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import persona.core.contracts.api.LauncherCheck
import persona.core.contracts.api.LauncherCheckAnswer
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.PaseoAgent
import persona.core.contracts.api.ClaudeDir
import persona.core.contracts.api.ClaudeDirsState
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.ImportedProvider
import persona.core.contracts.api.LauncherImportResult
import persona.core.contracts.api.NewProvider
import persona.core.contracts.api.ProviderPreset
import persona.core.contracts.api.ProviderSetup
import java.io.File
import java.nio.file.Files

/** The pure parts of the and-settings parity pages (#3048): "Blocked config directories", "Model providers", "Import from ~/.zshrc", "Storage". */
class ParityLogicTest {
    private val home = "/home/me"
    private val dirs = ClaudeDirsState(
        dirs = listOf(
            ClaudeDir("/home/me/.claude", isDefault = true, source = "default"),
            ClaudeDir("/home/me/.bot", blocked = true, blockedBy = "~/.bot"),
            ClaudeDir("/home/me/.automation/a", blocked = true, blockedBy = "~/.automation/*"),
            ClaudeDir("/srv/claude"),
        ),
        blocked = listOf("~/.bot", "~/.automation/*", "~/.gone"), home = home, live = true)

    @Test fun claudeDirsShortPathsAndToggles() {
        assertEquals("~/.claude", ClaudeDirsLogic.shortPath("/home/me/.claude", home))
        assertEquals("/home/user", ClaudeDirsLogic.shortPath("/home/user", home))
        assertEquals("/srv/claude", ClaudeDirsLogic.shortPath("/srv/claude", null))
        // Blocking adds the short path; unblocking lifts the rule (a glob lifts all it covers).
        assertEquals(dirs.blocked + "/srv/claude", ClaudeDirsLogic.toggled(dirs, dirs.dirs[3]))
        assertEquals(listOf("~/.bot", "~/.gone"), ClaudeDirsLogic.toggled(dirs, dirs.dirs[2]))
        assertEquals(listOf("~/.automation/*", "~/.gone"), ClaudeDirsLogic.toggled(dirs, dirs.dirs[1]))
        // A rule that blocks nothing listed shows on its own.
        assertEquals(listOf("~/.gone"), ClaudeDirsLogic.looseRules(dirs))
    }

    @Test fun claudeDirsTypedRules() {
        assertEquals("~/.x", ClaudeDirsLogic.cleaned("  ~/.x// "))
        assertEquals(ClaudeDirsLogic.Problem.Empty, ClaudeDirsLogic.problem("  ", emptyList()))
        assertEquals(ClaudeDirsLogic.Problem.Relative, ClaudeDirsLogic.problem("bots/*", emptyList()))
        // As iOS: a bare ~ is not a ~/ path.
        assertEquals(ClaudeDirsLogic.Problem.Relative, ClaudeDirsLogic.problem("~", emptyList()))
        assertEquals(ClaudeDirsLogic.Problem.Whole, ClaudeDirsLogic.problem("/", emptyList()))
        assertEquals(ClaudeDirsLogic.Problem.Taken, ClaudeDirsLogic.problem("~/.bot/", listOf("~/.bot")))
        assertNull(ClaudeDirsLogic.problem("~/.automation/*", emptyList()))
    }

    private val launchers = listOf(
        HubLauncher("team", source = "persona", model = "example-pro", baseUrl = "https://gateway.example.com/v1"),
        HubLauncher("cc_work", source = "zshrc", machines = listOf("mac")),
        HubLauncher("cc_old", source = "zshrc", hidden = true),
    )

    @Test fun providerGroupsAndSearch() {
        val all = ProviderGroups.of(launchers)
        assertEquals(listOf("team"), all.managed.map { it.name })
        assertEquals(listOf("cc_work"), all.zshrc.map { it.name })
        assertEquals(listOf("cc_old"), all.hidden.map { it.name })
        assertEquals(listOf("team"), ProviderGroups.of(launchers, "GATEWAY.example").managed.map { it.name })
        assertEquals(listOf("cc_work"), ProviderGroups.of(launchers, "laptop") { if (it == "mac") "Laptop" else it }.zshrc.map { it.name })
        val traeCli = HubLauncher("traex", agent = "traex")
        val paseoTraeCli = HubLauncher("traecli", agent = PaseoAgent)
        val customTraeCli = HubLauncher("trae-cli", agent = PaseoAgent, displayName = "Team CLI")
        val withTraeCli = launchers + listOf(traeCli, paseoTraeCli, customTraeCli)
        assertEquals(listOf(traeCli, paseoTraeCli), ProviderGroups.of(withTraeCli, "TRAE CLI").zshrc)
        assertEquals(listOf(traeCli), ProviderGroups.of(withTraeCli, "traex").zshrc)
        assertEquals(listOf(customTraeCli), ProviderGroups.of(withTraeCli, "Team CLI").zshrc)
        assertEquals("TRAE CLI", harnessName("trae-cli"))
        assertTrue(ProviderGroups.of(launchers, "nothing").isEmpty)
        assertEquals("gateway.example.com", providerHost("https://gateway.example.com/v1"))
        assertNull(providerHost(""))
    }

    private class Fake(var fail: Boolean = false) : ProviderSetup {
        val calls = mutableListOf<String>()
        val previews = mutableListOf<Pair<String, String?>>()
        var list = listOf(
            HubLauncher("team", source = "persona"),
            HubLauncher("cc_work", source = "zshrc"),
        )
        override suspend fun presets() = emptyList<ProviderPreset>()
        override suspend fun add(provider: NewProvider) {}
        override suspend fun launchers() = list
        override suspend fun remove(name: String) { calls += "remove $name"; if (fail) error("refused") }
        override suspend fun restore(name: String) { calls += "restore $name"; if (fail) error("refused") }
        override suspend fun importPreview(machine: String, alias: String?) = listOf(ImportedProvider("a"), ImportedProvider("b")).also { previews += machine to alias }
        override suspend fun import(machine: String, providers: List<String>, alias: String?) = LauncherImportResult(created = providers).also { calls += "import $machine $providers" + (alias?.let { " $it" }.orEmpty()) }
    }

    @Test fun providersHideRestoreDeleteMoveAtOnceAndComeBackOnFailure() = runBlocking {
        val api = Fake()
        val model = ProvidersModel(api)
        model.load()
        assertTrue(model.setHidden(api.list[1], true))
        assertTrue(model.state.value.launchers!!.first { it.name == "cc_work" }.hidden)
        // A Persona one is deleted, never hidden.
        assertFalse(model.setHidden(api.list[0], true))
        assertTrue(model.delete(api.list[0]))
        assertEquals(listOf("cc_work"), model.state.value.launchers!!.map { it.name })
        assertEquals(listOf("remove cc_work", "remove team"), api.calls)

        val failing = Fake(fail = true)
        val other = ProvidersModel(failing)
        other.load()
        assertFalse(other.delete(failing.list[0]))
        assertEquals(listOf("team", "cc_work"), other.state.value.launchers!!.map { it.name })
        assertTrue(other.state.value.notice != null)
    }

    @Test fun importTicksEverythingAndSendsWhatIsTicked() = runBlocking {
        val api = Fake()
        val model = ProviderImportModel(api)
        model.choose("mac")
        model.load()
        assertEquals(ImportPhase.Preview, model.state.value.phase)
        assertEquals(setOf("a", "b"), model.state.value.picked)
        model.toggle("a")
        model.runImport()
        assertEquals(ImportPhase.Done, model.state.value.phase)
        assertEquals(listOf("import mac [b]"), api.calls)
    }

    @Test fun oneFoundAliasStaysSelectedOnPreviewAndImport() = runBlocking {
        val api = Fake()
        val model = ProviderImportModel(api)
        model.choose("mac", "cc work")
        model.load()
        model.runImport()
        assertEquals(listOf("mac" to "cc work"), api.previews)
        assertEquals(listOf("import mac [a, b] cc work"), api.calls)
        model.restart()
        assertEquals("cc work", model.state.value.alias)
    }

    @Test fun checksRunOnEveryMachineAtOnceAndEachRowFillsInOnItsOwn() = runBlocking {
        val answers = mapOf("a" to CompletableDeferred<LauncherCheck>(), "b" to CompletableDeferred<LauncherCheck>())
        val started = mutableListOf<String?>()
        val api = object : ProviderSetup by Fake() {
            override suspend fun check(name: String, machine: String?): LauncherCheckAnswer {
                started += machine
                return LauncherCheckAnswer(machine, answers.getValue(machine!!).await())
            }
        }
        val model = ProviderDetailModel(api, "team")
        val job = launch { model.check(listOf("a", "b")) }
        withTimeout(2_000) { while (started.size < 2) yield() }
        // Both asked before either answered.
        assertEquals(listOf("a", "b"), started)
        answers.getValue("b").complete(LauncherCheck(ok = true))
        withTimeout(2_000) { model.state.first { it.checks["b"] != null } }
        assertNull(model.state.value.checks["a"])
        answers.getValue("a").complete(LauncherCheck(ok = false))
        job.join()
        assertEquals(false, model.state.value.checks["a"]?.ok)
    }

    @Test fun importFromAnOutdatedMachineSaysToRedeployIt() = runBlocking {
        fun model(failure: Exception) = ProviderImportModel(object : ProviderSetup by Fake() {
            override suspend fun importPreview(machine: String, alias: String?): List<ImportedProvider> = throw failure
        }).apply { choose("mac") }
        val outdated = model(HubFailure(HubError.Server, "too old", status = 409, code = "node_outdated"))
        outdated.load()
        assertEquals(R.string.settings_providers_import_outdated, outdated.state.value.error?.id)
        val other = model(HubFailure(HubError.Server, "conflict", status = 409, code = "busy"))
        other.load()
        assertEquals(R.string.settings_providers_import_read_failed, other.state.value.error?.id)
    }

    @Test fun storageKeepsAppDataAndClearsCaches() {
        val root = Files.createTempDirectory("storage").toFile()
        try {
            val cache = File(root, "cache").apply { mkdirs() }
            val files = File(root, "files").apply { mkdirs() }
            fun write(dir: File, path: String, bytes: Int) = File(dir, path).apply { parentFile!!.mkdirs(); writeBytes(ByteArray(bytes)) }
            write(cache, "hub-data/h/1/images/a.png", 100)
            write(cache, "hub-data/h/1/inbox-media/b.jpg", 200)
            write(cache, "hub-data/h/1/items.db", 300)
            write(cache, "inbox-downloads/c.pdf", 50)
            write(cache, "chat-uploads/d.png", 70)
            write(cache, "markdown-render/e.png", 30)
            write(files, "settings.json", 10)
            write(cache, "WebView/Default/HTTP Cache/f", 40)
            write(cache, "files-pdf/g.pdf", 5)
            write(cache, "files-share/h.txt", 5)
            write(cache, "shared/i.png", 5)
            write(cache, "persona-update-1.apk", 5)
            val downloading = write(cache, "hub-data/h/1/inbox-media/cache-1.part", 20)
            val inUse = write(cache, "hub-data/h/1/images/open.png", 8)
            // The data layer keeps what it has open (CacheClearing): here, one pinned image.
            val usage = StorageUsage(cache, listOf(files)) { list -> list.filter { it != inUse }.forEach { it.delete() } }
            val sizes = usage.scan()
            assertEquals(108L, sizes[StorageKind.ChatImages])
            assertEquals(270L, sizes[StorageKind.InboxMedia])
            assertEquals(30L, sizes[StorageKind.Other])
            assertEquals(40L, sizes[StorageKind.Web])
            assertEquals(400L, sizes[StorageKind.AppData])
            usage.clear(StorageKind.entries.toSet())
            val after = usage.scan()
            assertEquals(0L, after.getValue(StorageKind.Other))
            assertEquals(8L, after[StorageKind.ChatImages])
            assertEquals(20L, after[StorageKind.InboxMedia])
            assertTrue(inUse.exists() && downloading.exists())
            // The WebView's files are left to WebView.clearCache.
            assertEquals(40L, after[StorageKind.Web])
            // The open database, an attachment still to send, held-open files and the app's own files stay.
            assertEquals(400L, after[StorageKind.AppData])
            assertTrue(File(cache, "hub-data/h/1/items.db").exists())
        } finally { root.deleteRecursively() }
    }
}
