package persona.feature.settings

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.FeishuBotOwner
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.FeishuBotStatus
import persona.core.contracts.api.FeishuBotUpdate
import persona.core.designsystem.UiText
import persona.core.hub.HubError
import persona.core.hub.HubFailure

private class FakeBot(var status: FeishuBotStatus) : FeishuBotSettings {
    val updates = mutableListOf<FeishuBotUpdate>()
    var refuse: String? = null
    var beforeUpdate: (suspend () -> Unit)? = null
    var read: (suspend () -> FeishuBotStatus)? = null
    override suspend fun status() = read?.invoke() ?: status
    override suspend fun update(update: FeishuBotUpdate): FeishuBotStatus {
        updates += update
        beforeUpdate?.invoke()
        refuse?.let { throw HubFailure(HubError.Server, it, status = 400) }
        status = status.copy(enabled = update.enabled ?: status.enabled, appId = update.appId ?: status.appId,
            domain = update.domain ?: status.domain, hasSecret = status.hasSecret || !update.appSecret.isNullOrEmpty(), fromConfig = false)
        return status
    }
}

/** Settings › Lark bot (iOS `FeishuBotModelTests`): loading, the switch, saving credentials and what the hub says back. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class FeishuBotModelTest {
    @Test fun loadingFillsTheFormFromTheHub() = runBlocking {
        // The hub sends the domain it resolved (feishubot.ResolveDomain), the picker shows Feishu / Lark.
        val model = FeishuBotModel(FakeBot(FeishuBotStatus(enabled = true, appId = "cli_a1", domain = "https://larksuite.com", hasSecret = true)))
        model.load()
        val state = model.state.value
        assertEquals("cli_a1", state.appId)
        assertEquals("lark", state.domain)
        assertEquals("", state.appSecret)
        assertFalse(state.canSave) // nothing changed yet
    }

    @Test fun credentialsAreSavedTogetherAndTheSecretIsNotKept() = runBlocking {
        val bot = FakeBot(FeishuBotStatus())
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appId = " cli_new ")
        assertFalse(model.state.value.canSave) // a first save needs the secret too
        model.edit(appSecret = "s3cret")
        assertTrue(model.state.value.canSave)
        assertTrue(model.saveCredentials())
        assertEquals(FeishuBotUpdate(appId = "cli_new", appSecret = "s3cret", domain = "feishu"), bot.updates.single())
        val state = model.state.value
        assertEquals("", state.appSecret)
        assertTrue(state.status!!.hasSecret)
        assertFalse(state.canSave)
        // Saved once: the app id alone can change, the secret stays as it is on the hub.
        model.edit(domain = "lark")
        assertTrue(model.state.value.canSave)
    }

    @Test fun theHubsReasonIsShownWhenItCantVerify() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(hasSecret = true, appId = "cli_a1")).apply { refuse = "飞书说 App Secret 不对" }
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appSecret = "wrong")
        assertFalse(model.saveCredentials())
        assertEquals(listOf<Any>(UiText.Plain("飞书说 App Secret 不对")), model.state.value.error!!.args)
        assertEquals("wrong", model.state.value.appSecret) // kept to fix
    }

    @Test fun theSwitchSendsOnlyItself() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(hasSecret = true, appId = "cli_a1"))
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appId = "cli_editing")
        assertTrue(model.setEnabled(true))
        assertEquals(FeishuBotUpdate(enabled = true), bot.updates.single())
        assertTrue(model.state.value.status!!.enabled)
        assertEquals("cli_editing", model.state.value.appId) // what is being typed stays
    }

    @Test(timeout = 10_000) fun anOlderRefreshCannotUndoAnAcknowledgedSwitchOrItsCredentialDraft() = runBlocking {
        val original = FeishuBotStatus(appId = "cli_saved", hasSecret = true)
        val bot = FakeBot(original)
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appId = "cli_draft", appSecret = "draft-secret", domain = "lark")
        val response = CompletableDeferred<FeishuBotStatus>()
        bot.read = { response.await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        assertTrue(model.state.value.loading)
        assertFalse(refresh.isCompleted)

        // GET is still pending: the screen permits the switch, and PUT is acknowledged first.
        assertTrue(model.setEnabled(true))
        val confirmed = bot.status
        assertEquals(confirmed, model.state.value.status)
        response.complete(original)
        refresh.join()

        val state = model.state.value
        assertEquals(confirmed, state.status)
        assertEquals("cli_draft", state.appId)
        assertEquals("draft-secret", state.appSecret)
        assertEquals("lark", state.domain)
        assertFalse(state.loading)
        assertFalse(state.saving)
        assertNull(state.error)
        assertEquals(listOf(FeishuBotUpdate(enabled = true)), bot.updates)
    }

    @Test(timeout = 10_000) fun anOlderReadFailureCannotReplaceSaveSuccessAndTheNextRefreshStillWorks() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(appId = "cli_saved", hasSecret = true))
        val model = FeishuBotModel(bot)
        model.load()
        val response = CompletableDeferred<FeishuBotStatus>()
        bot.read = { response.await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        assertTrue(model.setEnabled(true))
        response.completeExceptionally(HubFailure(HubError.Server, "old read failed", status = 503))
        refresh.join()
        assertEquals(bot.status, model.state.value.status)
        assertNull(model.state.value.error)
        assertFalse(model.state.value.loading)

        bot.read = null
        bot.status = bot.status.copy(enabled = false, appId = "cli_remote", domain = "lark")
        model.load()
        assertEquals(bot.status, model.state.value.status)
        assertEquals("cli_remote", model.state.value.appId)
        assertEquals("lark", model.state.value.domain)
        assertTrue(model.setEnabled(true))
        assertTrue(model.setEnabled(false))
        assertEquals(listOf(true, true, false), bot.updates.map { it.enabled })
        assertFalse(model.state.value.saving)
    }

    @Test(timeout = 10_000) fun anOlderRefreshCannotRestoreCredentialsAfterTheyWereSaved() = runBlocking {
        val original = FeishuBotStatus(appId = "cli_old", hasSecret = true)
        val bot = FakeBot(original)
        val model = FeishuBotModel(bot)
        model.load()
        val response = CompletableDeferred<FeishuBotStatus>()
        bot.read = { response.await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        model.edit(appId = "cli_new", appSecret = "new-secret", domain = "lark")
        assertTrue(model.saveCredentials())
        val confirmed = model.state.value
        response.complete(original)
        refresh.join()
        assertEquals(confirmed.copy(loading = false), model.state.value)
        assertEquals("cli_new", model.state.value.appId)
        assertEquals("", model.state.value.appSecret)
        assertFalse(model.state.value.changed)
        assertEquals(listOf(FeishuBotUpdate(appId = "cli_new", appSecret = "new-secret", domain = "lark")), bot.updates)
    }

    @Test(timeout = 10_000) fun aReadStartedDuringSaveCannotPublishBeforeOrAfterItsAcknowledgement() = runBlocking {
        for (readFinishesFirst in listOf(true, false)) {
            val original = FeishuBotStatus(appId = "cli_saved", hasSecret = true)
            val bot = FakeBot(original)
            val model = FeishuBotModel(bot)
            model.load()
            model.edit(appSecret = "draft-secret", domain = "lark")
            val write = CompletableDeferred<Unit>()
            bot.beforeUpdate = { write.await() }
            val saving = async(start = CoroutineStart.UNDISPATCHED) { model.setEnabled(true) }
            assertTrue(model.state.value.saving)
            assertFalse(model.setEnabled(false)) // Duplicate writes still use the existing save guard.
            val read = CompletableDeferred<FeishuBotStatus>()
            bot.read = { read.await() }
            val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
            val stale = original.copy(appId = "cli_stale", enabled = false)
            if (readFinishesFirst) {
                read.complete(stale)
                refresh.join()
                assertEquals(original, model.state.value.status)
                assertTrue(model.state.value.saving)
            }
            write.complete(Unit)
            assertTrue(saving.await())
            if (!readFinishesFirst) {
                read.complete(stale)
                refresh.join()
            }
            assertEquals(bot.status, model.state.value.status)
            assertEquals("draft-secret", model.state.value.appSecret)
            assertEquals("lark", model.state.value.domain)
            assertFalse(model.state.value.saving)
            assertFalse(model.state.value.loading)
            assertEquals(listOf(FeishuBotUpdate(enabled = true)), bot.updates)
            bot.read = null
            model.load()
            assertEquals(bot.status, model.state.value.status)
        }
    }

    @Test(timeout = 10_000) fun aFailedSaveKeepsItsErrorAndDraftAcrossAnOlderReadAndCanBeRetried() = runBlocking {
        val original = FeishuBotStatus(appId = "cli_saved", hasSecret = true)
        val bot = FakeBot(original)
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appSecret = "draft-secret")
        val read = CompletableDeferred<FeishuBotStatus>()
        bot.read = { read.await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        bot.refuse = "save refused"
        assertFalse(model.setEnabled(true))
        val error = model.state.value.error
        assertNotNull(error)
        read.complete(original.copy(appId = "cli_stale"))
        refresh.join()
        assertEquals(original, model.state.value.status)
        assertEquals(error, model.state.value.error)
        assertEquals("draft-secret", model.state.value.appSecret)
        assertFalse(model.state.value.saving)
        assertFalse(model.state.value.loading)
        bot.refuse = null
        assertTrue(model.setEnabled(true))
        bot.read = null
        model.load()
        assertEquals(bot.status, model.state.value.status)
        assertNull(model.state.value.error)
        assertEquals("draft-secret", model.state.value.appSecret)
        assertEquals(listOf(FeishuBotUpdate(enabled = true), FeishuBotUpdate(enabled = true)), bot.updates)
    }

    @Test(timeout = 10_000) fun onlyTheLatestRefreshCanPublishItsStatusOrError() = runBlocking {
        for (olderFails in listOf(false, true)) {
            val bot = FakeBot(FeishuBotStatus(appId = "cli_saved", hasSecret = true))
            val model = FeishuBotModel(bot)
            model.load()
            val old = CompletableDeferred<FeishuBotStatus>()
            bot.read = { old.await() }
            val older = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
            val latest = CompletableDeferred<FeishuBotStatus>()
            bot.read = { latest.await() }
            val newer = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
            val current = bot.status.copy(appId = "cli_latest", enabled = true)
            latest.complete(current)
            newer.join()
            assertFalse(model.state.value.loading)
            if (olderFails) old.completeExceptionally(HubFailure(HubError.Server, "stale failure", status = 503))
            else old.complete(bot.status)
            older.join()
            assertEquals(current, model.state.value.status)
            assertEquals("cli_latest", model.state.value.appId)
            assertNull(model.state.value.error)
            assertTrue(bot.updates.isEmpty())
        }
    }

    @Test(timeout = 10_000) fun anOlderRefreshCannotEndTheLatestLoadingIndicator() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(appId = "cli_saved", hasSecret = true))
        val model = FeishuBotModel(bot)
        model.load()
        val old = CompletableDeferred<FeishuBotStatus>()
        bot.read = { old.await() }
        val older = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        val latest = CompletableDeferred<FeishuBotStatus>()
        bot.read = { latest.await() }
        val newer = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        old.complete(bot.status.copy(appId = "cli_stale"))
        older.join()
        assertTrue(model.state.value.loading)
        assertEquals("cli_saved", model.state.value.appId)
        latest.complete(bot.status.copy(appId = "cli_latest"))
        newer.join()
        assertFalse(model.state.value.loading)
        assertEquals("cli_latest", model.state.value.appId)
        assertTrue(bot.updates.isEmpty())
    }

    @Test(timeout = 10_000) fun aFailedLatestRefreshKeepsItsErrorUntilAFreshRetry() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(appId = "cli_saved", hasSecret = true))
        val model = FeishuBotModel(bot)
        model.load()
        val old = CompletableDeferred<FeishuBotStatus>()
        bot.read = { old.await() }
        val older = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        bot.read = { throw HubFailure(HubError.Server, "latest read failed", status = 503) }
        model.load()
        val error = model.state.value.error
        assertNotNull(error)
        assertFalse(model.state.value.loading)
        old.complete(bot.status.copy(appId = "cli_stale"))
        older.join()
        assertEquals(error, model.state.value.error)
        assertEquals(bot.status, model.state.value.status)
        bot.read = null
        bot.status = bot.status.copy(enabled = true, appId = "cli_recovered")
        model.load()
        assertEquals(bot.status, model.state.value.status)
        assertEquals("cli_recovered", model.state.value.appId)
        assertNull(model.state.value.error)
        assertFalse(model.state.value.loading)
        assertTrue(bot.updates.isEmpty())
    }

    @Test(timeout = 10_000) fun cancelledReadsAndWritesSettleAndAllowNewRequests() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(appId = "cli_saved", hasSecret = true))
        val model = FeishuBotModel(bot)
        model.load()
        bot.read = { CompletableDeferred<FeishuBotStatus>().await() }
        val refresh = launch(start = CoroutineStart.UNDISPATCHED) { model.load() }
        refresh.cancelAndJoin()
        assertTrue(refresh.isCancelled)
        assertFalse(model.state.value.loading)
        assertNull(model.state.value.error)
        bot.beforeUpdate = { CompletableDeferred<Unit>().await() }
        val saving = launch(start = CoroutineStart.UNDISPATCHED) { model.setEnabled(true) }
        saving.cancelAndJoin()
        assertTrue(saving.isCancelled)
        assertFalse(model.state.value.saving)
        assertNull(model.state.value.error)
        bot.beforeUpdate = null
        bot.read = null
        assertTrue(model.setEnabled(true))
        model.load()
        assertEquals(bot.status, model.state.value.status)
        assertEquals(listOf(FeishuBotUpdate(enabled = true), FeishuBotUpdate(enabled = true)), bot.updates)
    }

    @Test(timeout = 10_000) fun anEarlierApiModelsResponseCannotChangeTheCurrentApisDraft() = runBlocking {
        val oldBot = FakeBot(FeishuBotStatus(appId = "cli_previous", hasSecret = true))
        val oldModel = FeishuBotModel(oldBot)
        oldModel.load()
        val read = CompletableDeferred<FeishuBotStatus>()
        oldBot.read = { read.await() }
        val oldRefresh = launch(start = CoroutineStart.UNDISPATCHED) { oldModel.load() }
        // The screen remembers the model by API, so a different pairing gets a separate model.
        val bot = FakeBot(FeishuBotStatus(appId = "cli_current", hasSecret = true))
        val model = FeishuBotModel(bot)
        model.load()
        model.edit(appId = "cli_draft", appSecret = "current-secret", domain = "lark")
        assertTrue(model.setEnabled(true))
        val current = model.state.value
        read.complete(oldBot.status.copy(appId = "cli_previous_remote"))
        oldRefresh.join()
        assertEquals(current, model.state.value)
        assertEquals(listOf(FeishuBotUpdate(enabled = true)), bot.updates)
        assertTrue(oldBot.updates.isEmpty())
    }

    @Test fun theResolvedDomainMapsBackToThePicker() {
        assertEquals("feishu", FeishuBotState.domainChoice("https://open.feishu.cn"))
        assertEquals("feishu", FeishuBotState.domainChoice(""))
        assertEquals("lark", FeishuBotState.domainChoice("https://larksuite.com"))
        assertEquals("https://open.example.com", FeishuBotState.domainChoice("https://open.example.com"))
        val same = FeishuBotState(status = FeishuBotStatus(appId = "cli_a1", domain = "https://open.feishu.cn", hasSecret = true),
            appId = "cli_a1", domain = "feishu")
        assertFalse(same.canSave) // Feishu is what the hub already uses
    }

    @Test fun credentialsFromHubJsonNeedTheSecretTyped() {
        // hub.json's secret isn't the hub's to reuse for a saved copy: updateBot answers needs_secret.
        val fromConfig = FeishuBotState(status = FeishuBotStatus(appId = "cli_a1", domain = "https://open.feishu.cn", hasSecret = true, fromConfig = true),
            appId = "cli_a1", domain = "lark")
        assertFalse(fromConfig.canSave)
        assertTrue(fromConfig.copy(appSecret = "s3cret").canSave)
    }

    @Test fun anotherAppIdNeedsItsSecretAndSaysThePairingGoes() {
        val paired = FeishuBotState(status = FeishuBotStatus(appId = "cli_a1", hasSecret = true, owner = FeishuBotOwner("示例用户", 1)),
            appId = "cli_other")
        assertFalse(paired.canSave) // the old secret won't verify another app
        assertTrue(paired.unpairs)
        assertTrue(paired.copy(appSecret = "s3cret").canSave)
        assertFalse(paired.copy(appId = "cli_a1", domain = "lark").unpairs)
    }

    @Test fun aRefreshTakesTheHubsCredentialsUnlessBeingTyped() = runBlocking {
        val bot = FakeBot(FeishuBotStatus(hasSecret = true, appId = "cli_old"))
        val model = FeishuBotModel(bot)
        model.load()
        bot.status = bot.status.copy(appId = "cli_new") // changed on the web
        model.load()
        assertEquals("cli_new", model.state.value.appId)
        model.edit(appId = "cli_typing")
        bot.status = bot.status.copy(appId = "cli_newer")
        model.load()
        assertEquals("cli_typing", model.state.value.appId)
    }
}
