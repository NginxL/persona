package persona.platform.shortcuts

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.contracts.api.InteractionCenter

/** #4055 AC2: the launcher approval dialog must not turn restore-pending into a NotPaired decision. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ApproveFirstPairingRestoreTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun loadWaitsForRestoreBeforeDecidingWhetherTheHubIsPaired() = runTest {
        val restored = CompletableDeferred<Unit>()
        var paired = false
        val center = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val model = ApproveFirstModel(res, { paired }, center, { emptyList() },
            restore = { restored.await() })

        val load = async { model.load() }
        runCurrent()
        assertEquals("The dialog must stay loading while restoration is pending", ApproveState.Loading, model.state.value)

        paired = true
        restored.complete(Unit)
        advanceUntilIdle()
        load.await()
        assertEquals("The original load must continue against the restored pairing", ApproveState.Nothing, model.state.value)
    }
}
