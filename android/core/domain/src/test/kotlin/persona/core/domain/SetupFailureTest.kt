package persona.core.domain

import org.junit.Assert.assertEquals
import org.junit.Test

/** The main conversation's coded refusals (docs/assistant/onboarding.md 4.6): each names its step, none says the hub is gone. */
class SetupFailureTest {
    @Test fun eachCodeOpensItsStep() {
        val cases = mapOf(
            "no_machine" to (FailureReason.NoMachine to FailureAction.AddComputer),
            "machine_pending" to (FailureReason.MachinePending to FailureAction.ConfirmComputer),
            "node_unreachable" to (FailureReason.ComputerOffline to FailureAction.WakeComputer),
            "claude_missing" to (FailureReason.ClaudeMissing to FailureAction.InstallClaude),
            "launcher_missing" to (FailureReason.LauncherMissing to FailureAction.ConnectModel),
            "not_logged_in" to (FailureReason.NotLoggedIn to FailureAction.ConnectModel),
            "auth_failed" to (FailureReason.AuthFailed to FailureAction.EditProvider),
            "assistant_offline" to (FailureReason.AssistantRestarting to FailureAction.Retry),
            "launcher_out_of_scope" to (FailureReason.ProviderOutOfScope to FailureAction.EditProvider),
            "launcher_own_service" to (FailureReason.ProviderOutOfScope to FailureAction.EditProvider),
        )
        for ((code, expected) in cases) {
            val failure = mapOperationFailure(code, null, retryable = true)
            assertEquals(code, expected, failure.reason to failure.action)
        }
    }

    @Test fun anOfflineComputerIsNotAnUnreachableHub() {
        val failure = mapOperationFailure("node_unreachable", "none", retryable = true)
        assertEquals(FailureReason.ComputerOffline, failure.reason)
        assertEquals(false, failure.offline)
    }

    @Test fun deliveryEvidenceStillComesFirst() {
        assertEquals(FailureReason.Unknown, mapOperationFailure("no_machine", "unknown", retryable = true).reason)
    }

    /** A provider outside the computer's scope refuses the start before anything runs: the message waits for the user. */
    @Test fun aProviderOutOfScopeIsASetupRefusal() {
        assertEquals(true, "launcher_out_of_scope" in SetupFailureCodes)
        assertEquals(true, "launcher_own_service" in SetupFailureCodes)
    }
}
