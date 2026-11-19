package persona.feature.settings

import persona.core.contracts.api.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4

private val marketA = ExtensionMarket("3f2a9c1d0b", "https://plugins.example.com/persona/", "Example 扩展", extensions = 3,
    fetchedAt = "2026-11-04T06:00:00Z")
private val marketB = ExtensionMarket("8e01b7c4a2", "github:example/team-plugins", "团队扩展", hasToken = true, extensions = 2,
    warnings = listOf("第 3 个扩展：没有 manifest"))
private val runs = ExtensionPermission("runs_code", "在 hub 机器上运行程序")
private val fresh = CatalogExtension(marketA.id, "Example 扩展", "example-calendar", "Example 日历", "0.9.0", permissions = listOf(runs))
private val upgradable = CatalogExtension(marketA.id, id = "example-gateway", name = "Example 网关面板", version = "1.1.0", installed = "1.0.0",
    installedSource = "installed", fromThisMarket = true, upgradable = true)
private val elsewhere = CatalogExtension(marketA.id, id = "example-notes", name = "Example Notes", version = "1.2.0", installed = "1.2.0",
    installedSource = "installed")
private val builtin = CatalogExtension(marketA.id, id = "telegram", name = "Telegram", version = "2.0.0", installed = "202611041200",
    installedSource = "builtin")
private val broken = CatalogExtension(marketB.id, id = "team-windows", name = "Windows 工具", version = "0.2.0",
    problem = "没有这台 hub（linux-amd64）能用的包")
private val sameMarket = CatalogExtension(marketB.id, id = "team-digest", name = "每日摘要", version = "2.3.0", installed = "2.3.0",
    installedSource = "installed", fromThisMarket = true)

private class FakeMarket : ExtensionApiStub() {
    var markets = mutableListOf(marketA, marketB)
    var catalog = listOf(fresh, upgradable, elsewhere, broken)
    var fail: Exception? = null
    val calls = mutableListOf<String>()
    var gate: CompletableDeferred<Unit>? = null
    var preview = ExtensionPreview("pv1", PreviewManifest("example-calendar", "Example 日历", "0.9.0"), listOf(runs), "d-1")
    var removal = ExtensionRemoval()
    var installed = ExtensionDetail(HubExtension("example-calendar", "Example 日历", "connectors", "needs_setup", version = "0.9.0",
        needsSetup = listOf("密钥「API Key」")))

    private fun check() { fail?.let { throw it } }
    override suspend fun catalog(query: String, market: String?, refresh: Boolean): ExtensionCatalog {
        calls += "catalog q=$query market=$market refresh=$refresh"
        gate?.let { g -> if (query == "slow") g.await() }
        check()
        return ExtensionCatalog(markets, catalog.filter { (market == null || it.market == market) && (query.isEmpty() || query in it.name) }, "linux-amd64")
    }
    override suspend fun previewFromMarket(market: String, extension: String): ExtensionPreview { calls += "preview $market/$extension"; check(); return preview }
    override suspend fun install(preview: ExtensionPreview): ExtensionDetail { calls += "install ${preview.previewId} ${preview.permissionsDigest}"; check(); return installed }
    override suspend fun upgrade(id: String, preview: ExtensionPreview): ExtensionDetail { calls += "upgrade $id ${preview.previewId}"; check(); return installed }
    override suspend fun uninstall(id: String, keepData: Boolean, cascade: Boolean, members: Boolean): ExtensionRemoval { calls += "uninstall $id keep=$keepData"; check(); return removal }
    override suspend fun markets(): List<ExtensionMarket> { check(); return markets }
    override suspend fun addMarket(spec: String, token: String?): ExtensionMarket {
        calls += "add $spec token=${token != null}"; check()
        return ExtensionMarket("new${markets.size}", spec, "新市场", hasToken = token != null).also { markets += it }
    }
    override suspend fun removeMarket(id: String) { calls += "remove $id"; check(); markets.removeAll { it.id == id } }
    override suspend fun refreshMarket(id: String): ExtensionMarket { calls += "refresh $id"; check(); return markets.first { it.id == id }.copy(fetchedAt = "2026-11-04T07:05:00Z") }
    override suspend fun setMarketToken(id: String, token: String): ExtensionMarket { calls += "token $id ${token.isEmpty()}"; check(); return markets.first { it.id == id }.copy(hasToken = token.isNotEmpty()) }
}

/** Robolectric for the words: the app's strings in Simplified Chinese (robolectric.properties). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionMarketStateTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test fun catalogMetadataNeverInventsCountsOrLoadsInvalidLogoAddresses() {
        assertNull(catalogDownloads(res, null))
        assertNull(catalogDownloads(res, -1))
        assertEquals(2, installedSubtitleLines(catalogDownloads(res, null)))
        assertEquals(2, installedSubtitleLines(catalogDownloads(res, -1)))
        assertEquals(3, installedSubtitleLines(catalogDownloads(res, 0)))
        assertEquals("下载 0 次", catalogDownloads(res, 0))
        assertEquals("下载 4,294,967,296 次", catalogDownloads(res, 4294967296))
        assertEquals("https://extensions.example.com/logo.png", catalogLogoUrl("https://extensions.example.com/logo.png"))
        for (url in listOf(null, "", "http://example.com/logo.png", "/logo.png", "//example.com/logo.png", "https:///logo.png",
            "https://", "https://bad host/logo.png", "https://user:password@example.com/logo.png", "https://example.com:99999/logo.png")) {
            assertNull("Invalid URL must use the generic icon: $url", catalogLogoUrl(url))
        }
    }

    @Test fun downloadCountWordsFollowTheAppLanguageAndEnglishPluralRules() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        fun localized(tag: String) = context.createConfigurationContext(android.content.res.Configuration(res.configuration).apply {
            setLocale(java.util.Locale.forLanguageTag(tag))
        }).resources
        assertEquals("1 download", catalogDownloads(localized("en"), 1))
        assertEquals("0 downloads", catalogDownloads(localized("en"), 0))
        assertEquals("2 downloads", catalogDownloads(localized("en"), 2))
        assertEquals("下载 1 次", catalogDownloads(localized("zh-Hans"), 1))
        assertEquals("1回ダウンロード", catalogDownloads(localized("ja"), 1))
    }

    @Test fun catalogKeepsPopularityOrderThroughOfficialMarketNaming() = runBlocking {
        val popular = fresh.copy(name = "Zeta", downloads = 20)
        val lessPopular = fresh.copy(id = "alpha", name = "Alpha", downloads = 10)
        val zero = fresh.copy(id = "zero", downloads = 0)
        val old = fresh.copy(id = "old")
        val api = FakeMarket().apply { catalog = listOf(popular, lessPopular, zero, old); markets[0] = markets[0].copy(official = true) }
        val model = CatalogModel(api, res)
        model.load()
        assertEquals(listOf("example-calendar", "alpha", "zero", "old"), model.state.value.extensions.map { it.id })
        assertEquals(listOf(20L, 10L, 0L, null), model.state.value.extensions.map { it.downloads })
    }

    @Test fun whereEachExtensionStandsAndWhatThePageOffers() {
        assertEquals(CatalogStanding.Available, CatalogText.standing(fresh))
        assertEquals(listOf(CatalogAction.Install), CatalogText.actions(fresh))
        assertNull(CatalogText.badge(res, fresh))
        assertEquals("0.9.0", CatalogText.line(res, fresh))

        assertEquals(CatalogStanding.Upgradable, CatalogText.standing(upgradable))
        assertEquals(listOf(CatalogAction.Upgrade, CatalogAction.Uninstall), CatalogText.actions(upgradable))
        assertEquals("可升级", CatalogText.badge(res, upgradable))
        assertEquals("1.0.0 → 1.1.0", CatalogText.line(res, upgradable))

        // Installed from a folder on the hub's machine: the phone neither upgrades (not newer) nor removes it.
        assertEquals(CatalogStanding.Installed, CatalogText.standing(elsewhere))
        assertTrue(CatalogText.actions(elsewhere).isEmpty())
        assertTrue(CatalogText.noActionReason(res, elsewhere)!!.contains("personad extension"))

        assertEquals(CatalogStanding.InstalledElsewhere, CatalogText.standing(builtin))
        assertEquals("2.0.0 · 已装 202611041200（内置）", CatalogText.line(res, builtin))
        assertTrue(CatalogText.actions(builtin).isEmpty())

        assertEquals(CatalogStanding.Unavailable, CatalogText.standing(broken))
        assertEquals("装不了", CatalogText.badge(res, broken))
        assertEquals(broken.problem, CatalogText.line(res, broken))
        assertEquals(broken.problem, CatalogText.noActionReason(res, broken))
        assertTrue(CatalogText.actions(broken).isEmpty())
        // An upgradable one this hub cannot take is not offered.
        assertTrue(CatalogText.actions(upgradable.copy(problem = "要更新的 hub")).let { CatalogAction.Upgrade !in it })

        assertEquals(listOf(CatalogAction.Uninstall), CatalogText.actions(sameMarket))
        assertEquals("2.3.0 · 已安装", CatalogText.line(res, sameMarket))
    }

    @Test fun previewsSayWhatTheConfirmDoes() {
        val install = ExtensionPreview("p", permissionsDigest = "d")
        assertEquals("同意并安装", CatalogText.confirmLabel(res, install))
        assertFalse(CatalogText.asksForMore(install))
        val upgrade = install.copy(upgrade = true, from = "1.0.0")
        assertEquals("升级", CatalogText.confirmLabel(res, upgrade))
        val more = upgrade.copy(morePermissions = true, addedPermissions = listOf(ExtensionPermission("hub_api", "完整使用 hub")))
        assertEquals("同意新权限并升级", CatalogText.confirmLabel(res, more))
        assertTrue(CatalogText.asksForMore(more))
    }

    @Test fun marketWords() {
        assertEquals("3 个扩展 · 读取于 2026-11-04 06:00", CatalogText.marketLine(res, marketA))
        assertEquals("2 个扩展 · 有 token", CatalogText.marketLine(res, marketB))
        val done = ExtensionDetail(HubExtension("x", "X", version = "1.1.0"))
        assertEquals("已安装", CatalogText.installed(res, done, upgrade = false))
        assertEquals("已升级到 1.1.0", CatalogText.installed(res, done, upgrade = true))
        assertEquals("已安装，还缺：密钥「API Key」", CatalogText.installed(res, FakeMarket().installed, upgrade = false))
    }

    @Test fun aGitHubMarketWithoutATokenWantsOne() {
        assertTrue(wantsGitHubToken(marketB.copy(hasToken = false)))
        assertFalse(wantsGitHubToken(marketB))
        assertFalse(wantsGitHubToken(marketA))
        assertFalse(wantsGitHubToken(marketA.copy(spec = "git:https://github.com/example/repo")))
        assertTrue(wantsGitHubToken(marketA.copy(spec = " github:example/public ")))
    }

    @Test fun aTokenFromTheCatalogReadsTheMarketAgain() = runBlocking {
        val api = FakeMarket()
        val model = CatalogModel(api, res)
        model.load()
        assertTrue(model.setToken(marketB.id, "tok"))
        assertTrue("token ${marketB.id} false" in api.calls)
        assertTrue(api.calls.last().startsWith("catalog"))
        assertNull(model.state.value.tokenError)
        api.fail = HubFailure(HubError.Server, "token 不对", status = 400)
        assertFalse(model.setToken(marketB.id, "bad"))
        assertEquals("没有换成：token 不对", model.state.value.tokenError)
    }

    @Test fun theCatalogSearchesAndNarrowsToAMarket() = runBlocking {
        val api = FakeMarket()
        val model = CatalogModel(api, res)
        model.load()
        assertEquals(4, model.state.value.extensions.size)
        model.setQuery("日历")
        model.load()
        assertEquals(listOf("example-calendar"), model.state.value.extensions.map { it.id })
        model.setQuery("")
        model.setMarket(marketB.id)
        model.load()
        assertEquals(listOf("team-windows"), model.state.value.extensions.map { it.id })
        assertEquals("catalog q= market=${marketB.id} refresh=false", api.calls.last())
        model.load(refresh = true)
        assertTrue(api.calls.last().endsWith("refresh=true"))
        // A market that is gone is not a filter any more.
        api.markets.removeAll { it.id == marketB.id }
        model.load()
        assertNull(model.state.value.market)
    }

    @Test fun aLateAnswerForAnOlderQueryIsDropped() = runBlocking {
        val api = FakeMarket()
        api.gate = CompletableDeferred()
        val model = CatalogModel(api, res)
        model.setQuery("slow")
        val slow = async { model.load() }
        yield()
        model.setQuery("日历")
        model.load()
        api.gate!!.complete(Unit)
        slow.await()
        assertEquals(listOf("example-calendar"), model.state.value.extensions.map { it.id })
        assertFalse(model.state.value.loading)
    }

    @Test fun catalogFailuresSayWhy() = runBlocking {
        val api = FakeMarket()
        api.fail = HubFailure(HubError.Server, "坏了", status = 500)
        val model = CatalogModel(api, res)
        model.load()
        assertEquals("读取失败：坏了", model.state.value.error)
        assertNull(model.state.value.catalog)
    }

    @Test fun installsOnlyAfterThePreviewIsAgreedTo() = runBlocking {
        val api = FakeMarket()
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res)
        model.load()
        assertEquals("Example 日历", model.state.value.extension?.name)
        assertEquals("Example 扩展", model.state.value.marketName)
        assertTrue(model.preview())
        assertEquals("pv1", model.state.value.preview?.previewId)
        assertFalse(api.calls.any { it.startsWith("install") })
        api.catalog = api.catalog.map { if (it.id == "example-calendar") it.copy(installed = "0.9.0", installedSource = "installed", fromThisMarket = true) else it }
        assertEquals("已安装，还缺：密钥「API Key」", model.confirm())
        assertTrue("install pv1 d-1" in api.calls)
        assertNull(model.state.value.preview)
        assertEquals("0.9.0", model.state.value.extension?.installed)
        // Turning a preview down installs nothing.
        model.preview()
        model.dismissPreview()
        assertNull(model.confirm())
        assertEquals(1, api.calls.count { it.startsWith("install") })
    }

    @Test fun anUpgradePreviewUpgrades() = runBlocking {
        val api = FakeMarket()
        api.preview = api.preview.copy(upgrade = true, from = "1.0.0")
        val model = CatalogExtensionModel(api, marketA.id, "example-gateway", res)
        model.load()
        model.preview()
        model.confirm()
        assertTrue("upgrade example-gateway pv1" in api.calls)
    }

    /** A rename (#2511): a page opened under the old ID (the upgrade row, a link from before) finds the extension
     *  under its new one and upgrades the installed extension through the old ID, which the hub resolves. */
    @Test fun aRenamedExtensionIsFoundUnderItsOldId() = runBlocking {
        val api = FakeMarket()
        api.catalog = api.catalog.map { if (it.id == "example-gateway") it.copy(id = "example-panel", replaces = "example-gateway", renameFrom = "example-gateway") else it }
        api.preview = api.preview.copy(upgrade = true, from = "1.0.0")
        val model = CatalogExtensionModel(api, marketA.id, "example-gateway", res)
        model.load()
        assertFalse(model.state.value.missing)
        assertEquals("example-panel", model.state.value.extension?.id)
        model.preview()
        model.confirm()
        assertTrue("upgrade example-gateway pv1" in api.calls)
        val up = ExtensionUpgrade(id = "example-gateway", market = marketA.id, from = "1.0.0", to = "1.1.0", state = "needs_permission", renameTo = "example-panel")
        assertEquals("example-panel", up.marketId)
        assertEquals("example-gateway", up.copy(renameTo = null).marketId)
        // The row says the ID changes: the hub's words, or the app's for a version Check for updates found.
        assertEquals("ID changes", ExtensionText.upgradeDetail(res, up.copy(detail = "ID changes")))
        assertEquals(res.getString(R.string.settings_ext_upgrade_rename, "example-gateway", "example-panel"), ExtensionText.upgradeDetail(res, up))
    }

    @Test fun failuresKeepThePageAndSayWhy() = runBlocking {
        val api = FakeMarket()
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res)
        model.load()
        api.fail = HubFailure(HubError.Server, "sha256 不对", status = 400)
        assertFalse(model.preview())
        assertEquals("没有拿到安装包：sha256 不对", model.state.value.actionError)
        api.fail = null
        model.preview()
        api.fail = HubFailure(HubError.Server, "预览过期了，重新预览一次", status = 400)
        assertNull(model.confirm())
        assertEquals("没有安装：预览过期了，重新预览一次", model.state.value.actionError)
        assertNull(model.state.value.preview)
        assertNotNull(model.state.value.extension)
    }

    @Test fun anExpiredPreviewIsPreviewedAgainNotSent() = runBlocking {
        val api = FakeMarket()
        var clock = Instant.parse("2026-11-08T10:00:00Z")
        api.preview = api.preview.copy(expiresAt = "2026-11-08T10:10:00Z")
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res, now = { clock })
        model.load()
        model.preview()
        // The sheet was left open past the expiry: the yes is not sent, the hub would refuse it.
        clock = Instant.parse("2026-11-08T10:10:00Z")
        assertNull(model.confirm())
        assertFalse(api.calls.any { it.startsWith("install") })
        assertEquals("pv1", model.state.value.preview?.previewId)
        // Previewed again: a fresh preview from the hub, and the yes goes with it, never with the old one.
        api.preview = api.preview.copy(previewId = "pv2", permissionsDigest = "d-2", expiresAt = "2026-11-08T10:20:00Z")
        assertTrue(model.previewAgain())
        assertEquals(2, api.calls.count { it == "preview ${marketA.id}/example-calendar" })
        assertEquals("pv2", model.state.value.preview?.previewId)
        model.confirm()
        assertEquals(listOf("install pv2 d-2"), api.calls.filter { it.startsWith("install") })
    }

    @Test fun aBundleMemberThatExpiresFirstStopsTheYes() = runBlocking {
        val api = FakeMarket()
        api.preview = api.preview.copy(expiresAt = "2026-11-08T10:10:00Z", bundle = listOf(
            ExtensionPreview("member", PreviewManifest("example-base", "Example 基础", "1.0.0"), permissionsDigest = "d-m", expiresAt = "2026-11-08T10:05:00Z")))
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res, now = { Instant.parse("2026-11-08T10:06:00Z") })
        model.load()
        model.preview()
        assertNull(model.confirm())
        assertFalse(api.calls.any { it.startsWith("install") })
    }

    @Test fun aFailedPreviewAgainSaysWhyAndCanBeTriedAgain() = runBlocking {
        val api = FakeMarket()
        api.preview = api.preview.copy(expiresAt = "2026-11-08T10:10:00Z")
        var clock = Instant.parse("2026-11-08T10:00:00Z")
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res, now = { clock })
        model.load()
        model.preview()
        clock = Instant.parse("2026-11-08T10:11:00Z")
        api.fail = HubFailure(HubError.Server, "市场暂时连不上", status = 502)
        assertFalse(model.previewAgain())
        assertEquals("没有拿到安装包：市场暂时连不上", model.state.value.actionError)
        assertNull(model.state.value.preview)
        api.fail = null
        api.preview = api.preview.copy(previewId = "pv2", permissionsDigest = "d-2", expiresAt = "2026-11-08T10:21:00Z")
        assertTrue(model.preview())
        assertNull(model.state.value.actionError)
        model.confirm()
        assertEquals(listOf("install pv2 d-2"), api.calls.filter { it.startsWith("install") })
    }

    @Test fun theHubRefusingAPreviewTheClockCallsCurrentEndsItAndTheNextIsFresh() = runBlocking {
        val api = FakeMarket()
        // This phone's clock is behind: the preview still looks current here, the hub has already let it go.
        api.preview = api.preview.copy(expiresAt = "2026-11-08T10:10:00Z")
        val model = CatalogExtensionModel(api, marketA.id, "example-calendar", res, now = { Instant.parse("2026-11-08T10:01:00Z") })
        model.load()
        model.preview()
        api.fail = HubFailure(HubError.Server, "预览过期了，重新预览一次", status = 400)
        assertNull(model.confirm())
        assertEquals(listOf("install pv1 d-1"), api.calls.filter { it.startsWith("install") })
        assertEquals("没有安装：预览过期了，重新预览一次", model.state.value.actionError)
        assertNull(model.state.value.preview)
        api.fail = null
        api.preview = api.preview.copy(previewId = "pv2", permissionsDigest = "d-2")
        model.preview()
        model.confirm()
        assertEquals(listOf("install pv1 d-1", "install pv2 d-2"), api.calls.filter { it.startsWith("install") })
    }

    @Test fun removesAndSeesAExtensionGone() = runBlocking {
        val api = FakeMarket()
        val model = CatalogExtensionModel(api, marketA.id, "example-gateway", res)
        model.load()
        api.catalog = api.catalog.map { if (it.id == "example-gateway") it.copy(installed = null, fromThisMarket = false, upgradable = false) else it }
        assertTrue(model.uninstall(keepData = true))
        assertTrue("uninstall example-gateway keep=true" in api.calls)
        assertNull(model.state.value.extension?.installed)
        api.catalog = emptyList()
        model.load()
        assertTrue(model.state.value.missing)
    }

    @Test fun cleanupWarningsAreHeldUntilTheirOkAndBlankResultsStayQuiet() = runBlocking {
        val warnings = listOf("请手动删除数据目录", "缓存清理失败，请手动处理")
        val api = FakeMarket().apply { removal = ExtensionRemoval(warnings = warnings) }
        val model = CatalogExtensionModel(api, marketA.id, "example-gateway", res)
        model.load()
        assertTrue(model.uninstall(keepData = true))
        val notice = model.state.value.removalNotice!!
        assertEquals("已卸载 Example 网关面板", notice.title)
        assertEquals(warnings, notice.warnings)
        model.dismissRemovalNotice()
        assertNull(model.state.value.removalNotice)
        assertNull(model.removal)

        api.removal = ExtensionRemoval(warnings = listOf(" ", "\n"))
        assertTrue(model.uninstall(keepData = true))
        assertNull(model.state.value.removalNotice)
    }

    @Test fun cleanupWarningsDecodeFromDirectAndSteppedSuccessfulRemovals() {
        val json = kotlinx.serialization.json.Json { ignoreUnknownKeys = true }
        val warnings = listOf("data directory cleanup failed", "cache cleanup failed")
        val direct = json.decodeFromString(ExtensionRemoval.serializer(),
            """{"removed_members":["example-member"],"warnings":["data directory cleanup failed","cache cleanup failed"]}""")
        assertEquals(listOf("example-member"), direct.removedMembers)
        assertEquals(warnings, direct.warnings)
        val stepped = json.decodeFromString(ExtensionOperation.serializer(),
            """{"id":"remove","kind":"uninstall","state":"succeeded","warnings":["data directory cleanup failed","cache cleanup failed"]}""")
        assertEquals(warnings, stepped.warnings)
        val old = json.decodeFromString(ExtensionRemoval.serializer(), """{"removed_members":[]}""")
        assertNull(old.warnings)
        assertNull(extensionRemovalNotice(res, "Example", members = false, removal = old))
    }

    @Test fun marketRemovalCountsOnlyKnownAffectedExtensionsAndWordsItsFallbacks() {
        val ownOn = HubExtension("on", source = "installed", market = "m1", manager = "assistant", autoUpgrade = true)
        val ownOff = HubExtension("off", source = "installed", market = "m1", manager = "assistant", autoUpgrade = false)
        val otherManager = HubExtension("other-manager", source = "installed", market = "m1", manager = "other", autoUpgrade = true)
        val otherMarket = HubExtension("other-market", source = "installed", market = "m2", manager = "assistant", autoUpgrade = true)
        val notFromMarket = HubExtension("local", manager = "assistant", autoUpgrade = null)
        val configuredWithMarket = HubExtension("configured", source = "hub.json", market = "m1", autoUpgrade = true)
        val attachedWithMarket = HubExtension("attached", source = "installed", market = "m1", autoUpgrade = true,
            attached = kotlinx.serialization.json.JsonObject(emptyMap()))
        val installed = listOf(ownOn, ownOff, otherManager, otherMarket, notFromMarket, configuredWithMarket, attachedWithMarket)
        val roster = ManagerRoster(listOf(
            HubManager("assistant", "Default", online = true, isDefault = true),
            HubManager("other", "Other", online = true),
        ), "assistant")
        val defaultAssistant = CurrentAssistant(roster = roster, choice = AssistantManagerChoice.Choice(defaultId = "assistant"))
        val selectedAssistant = CurrentAssistant(roster = roster, choice = AssistantManagerChoice.Choice(manager = "other", defaultId = "assistant"))
        assertEquals(2, affectedExtensionCount("m1", installed, defaultAssistant))
        assertEquals(0, affectedExtensionCount("m1", listOf(otherMarket, notFromMarket), defaultAssistant))
        // An unstamped extension belongs to the default assistant, never to a selected non-default assistant.
        assertEquals(0, affectedExtensionCount("m1", listOf(ownOn.copy(manager = null)), selectedAssistant))
        assertEquals(0, affectedExtensionCount("m1", listOf(ownOn.copy(manager = null)), selectedAssistant.copy(roster = null)))
        assertEquals(1, affectedExtensionCount("m1", listOf(ownOn.copy(manager = null)), defaultAssistant))
        assertEquals(1, affectedExtensionCount("m1", listOf(ownOn.copy(manager = "other")), selectedAssistant))
        assertNull(affectedExtensionCount("m1", listOf(ownOn.copy(market = null)), defaultAssistant))
        assertNull(affectedExtensionCount("m1", installed, defaultAssistant.copy(roster = null)))

        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val en = context.createConfigurationContext(android.content.res.Configuration(res.configuration).apply {
            setLocale(java.util.Locale.ENGLISH)
        }).resources
        assertEquals("The hub stops reading this market. No installed extension came from it.", marketRemovalText(en, 0))
        assertEquals("The hub stops reading this market. 1 extension installed from it stays, but stops upgrading until the market is added again with the same address.", marketRemovalText(en, 1))
        assertEquals("The hub stops reading this market. 2 extensions installed from it stay, but stop upgrading until the market is added again with the same address.", marketRemovalText(en, 2))
        assertEquals(en.getString(R.string.settings_ext_remove_market_text), marketRemovalText(en, null))
    }

    @Test fun managesTheMarkets() = runBlocking {
        val api = FakeMarket()
        val model = MarketsModel(api, res)
        model.load()
        assertEquals(2, model.state.value.markets?.size)
        // #1373: the address goes to the hub as typed (git:, git@host:path.git, ssh://… and whatever it adds later).
        assertFalse(model.add("   ", ""))
        assertTrue(model.add("git@git.example.com:team/plugins.git", ""))
        assertEquals("add git@git.example.com:team/plugins.git token=false", api.calls.last())
        assertTrue(model.add(" github:example/more ", "  "))
        assertEquals("add github:example/more token=false", api.calls.last())
        assertTrue(model.add("github:example/private", "tok"))
        assertEquals("add github:example/private token=true", api.calls.last())
        assertEquals(5, model.state.value.markets?.size)
        assertTrue(model.refresh(marketA.id))
        assertEquals("2026-11-04T07:05:00Z", model.state.value.markets?.first { it.id == marketA.id }?.fetchedAt)
        assertTrue(model.setToken(marketB.id, ""))
        assertFalse(model.state.value.markets!!.first { it.id == marketB.id }.hasToken)
        assertTrue(model.remove(marketA.id))
        assertNull(model.state.value.markets?.firstOrNull { it.id == marketA.id })
    }

    @Test fun marketFailuresSayWhy() = runBlocking {
        val api = FakeMarket()
        val model = MarketsModel(api, res)
        model.load()
        api.fail = HubFailure(HubError.Server, "读不到 github:example/private：找不到（404）：地址不对，或者是私有的，要填 token", status = 400)
        assertFalse(model.add("github:example/private", ""))
        assertTrue(model.state.value.addError!!.contains("要填 token"))
        api.fail = HubFailure(HubError.Server, "token 不对", status = 400)
        assertFalse(model.setToken(marketB.id, "bad"))
        assertEquals("没有换成：token 不对", model.state.value.actionError)
        assertNull(model.state.value.working)
        assertEquals(2, model.state.value.markets?.size)
    }

    @Test fun theAutoUpgradeSwitchForEveryMarketExtension() = runBlocking {
        var current = true
        var refuse = false
        val api = object : ExtensionApiStub() {
            override suspend fun markets() = listOf(marketA)
            override suspend fun autoUpgrades() = current
            override suspend fun setAutoUpgrades(auto: Boolean): Boolean {
                if (refuse) throw HubFailure(HubError.Server, "hub 拒绝了", status = 500)
                current = auto
                return current
            }
        }
        val model = MarketsModel(api, res)
        model.load()
        assertEquals(true, model.state.value.autoUpgrade)
        assertTrue(model.setAutoUpgrade(false))
        assertEquals(false, model.state.value.autoUpgrade)
        refuse = true
        assertFalse(model.setAutoUpgrade(true))
        assertEquals(false, model.state.value.autoUpgrade)
        assertEquals("没有改：hub 拒绝了", model.state.value.actionError)
        // A hub without auto-upgrades: no switch, the markets still show.
        val old = MarketsModel(object : ExtensionApiStub() { override suspend fun markets() = listOf(marketA) }, res)
        old.load()
        assertNull(old.state.value.autoUpgrade)
        assertEquals(1, old.state.value.markets?.size)
    }

    @Test
    fun gitHubTokenPageIsFilledInAndAsksNoPermission() {
        val u = java.net.URI(GitHubTokenUrl)
        assertEquals("github.com", u.host)
        assertEquals("/settings/personal-access-tokens/new", u.path)
        val q = u.query.split("&").associate { it.substringBefore("=") to it.substringAfter("=") }
        assertTrue(q.getValue("name").isNotEmpty() && q.getValue("name").length <= 40)
        assertTrue(q.getValue("expires_in").toInt() in 1..366)
        assertFalse("contents" in q)
        assertTrue(isGitHubMarket(" github:example/extensions"))
        assertFalse(isGitHubMarket("https://example.com/extensions/"))
    }
}
