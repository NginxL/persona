package persona.feature.settings

import persona.core.contracts.ServerEvent
import persona.core.contracts.api.*
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.core.app.ApplicationProvider

@RunWith(AndroidJUnit4::class)
@org.robolectric.annotation.Config(sdk = [35])
class DiagnosticsStateTest {
    private val resources get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test fun observesLiveChangesAndReprobesTheRunningConnection() = runBlocking {
        val connection = LiveConnection()
        val pairing = object : SettingsDiagnostics {
            override suspend fun read(): ConnectionDiagnostics = error("Pairing snapshots must not be read")
            override suspend fun reprobe(): ConnectionDiagnostics = error("Pairing must not be probed")
            override suspend fun scheduleLockScreenTest(delaySeconds: Int) = Unit
        }
        val controller = DiagnosticsState(connection, connection, pairing)
        connection.diagnostics.value = ConnectionDiagnostics(currentPath = "域名 · https://example.com")
        controller.refresh()
        assertEquals(connection.diagnostics.value, controller.data.value)
        connection.diagnostics.value = connection.diagnostics.value.copy(
            lastReceivedAt = "2026-10-31T00:00:00Z", heartbeatSeconds = 20,
        )
        assertEquals("2026-10-31T00:00:00Z", controller.data.value.lastReceivedAt)
        assertEquals(20, controller.data.value.heartbeatSeconds)
        controller.refresh(reprobe = true)
        assertEquals(1, connection.reconnections)
        assertNull(controller.data.value.currentPath)
        connection.diagnostics.value = ConnectionDiagnostics(lastDisconnectReason = "连接中断")
        assertEquals("连接中断", controller.data.value.lastDisconnectReason)
        connection.failHistory = true
        controller.refresh()
        assertEquals("连接中断", controller.data.value.lastDisconnectReason)
        assertNotNull(controller.message.value)
        assertFalse(controller.busy.value)
    }

    @Test fun missingSourceAndUnmeasuredSamplesStayUnknown() = runBlocking {
        val connection = LiveConnection()
        connection.diagnostics.value = ConnectionDiagnostics(currentPath = "旧快照")
        val controller = DiagnosticsState(connection, null, null)
        controller.refresh()
        assertNull(controller.data.value.currentPath)
        assertNotNull(controller.message.value)
        controller.refresh(reprobe = true)
        assertEquals(1, connection.reconnections)
        val candidate = ConnectionCandidate("https://example.com", "name", persona.core.contracts.CoreText.plain("未实测"))
        assertEquals("未实测", candidate.probeMeasurement(resources))
        assertEquals("未实测", candidate.copy(probeDurationMillis = 12).probeMeasurement(resources))
        assertEquals("未实测", candidate.copy(probeDurationMillis = -1, sampleCount = 1).probeMeasurement(resources))
        assertEquals("12 毫秒（样本数 1），不作为性能基线", candidate.copy(probeDurationMillis = 12, sampleCount = 1).probeMeasurement(resources))
    }

    @Test fun aFailedPairingShowsWithoutARunningConnection() = runBlocking {
        val connection = LiveConnection()
        val attempts = PairingAttempts()
        val controller = DiagnosticsState(connection, connection, null, attempts)
        assertNull(controller.pairing.value)
        val request = PairingRequest("https://hub.example.com/persona", "secret-code", hub = "test-hub")
        val handshake = javax.net.ssl.SSLHandshakeException("PKIX path building failed").apply {
            initCause(java.security.cert.CertificateException("secret-code is not trusted"))
        }
        val error = PairingException(PairingFailure.Certificate).apply {
            initCause(persona.core.hub.HubFailure(persona.core.hub.HubError.CertificateMismatch, cause = handshake))
        }
        val candidates = listOf(ConnectionCandidate("https://hub.example.com/persona", "name", persona.core.contracts.CoreText.plain("证书指纹不符")))
        attempts.record(pairingAttempt(request, error, PairingStage.Probe, candidates, java.time.Instant.parse("2026-11-05T01:02:03Z")))
        val attempt = requireNotNull(controller.pairing.value)
        assertTrue(attempt.failed)
        assertEquals("https://hub.example.com/persona", attempt.address)
        assertEquals(PairingAttemptStep.Tls, attempt.step)
        assertEquals("2026-11-05T01:02:03Z", attempt.at)
        assertEquals(candidates, attempt.candidates)
        // The text names the failure, never the pairing code.
        assertTrue(attempt.error!!.contains("PKIX path building failed"))
        assertFalse(attempt.error!!.contains("secret-code"))
        assertEquals(listOf("地址", "结果", "错误", "时间"), attempt.rows(resources).map { it.first })
        assertEquals("失败，停在「安全证书」", attempt.rows(resources)[1].second)
        attempts.record(pairingAttempt(request, null))
        assertFalse(controller.pairing.value!!.failed)
        assertEquals("成功", controller.pairing.value!!.rows(resources)[1].second)
    }

    @Test fun pairingStepsNameWhereItStopped() {
        fun hub(kind: persona.core.hub.HubError, status: Int? = null) = persona.core.hub.HubFailure(kind, status = status)
        assertEquals(PairingAttemptStep.Parse, pairingStep(PairingException(PairingFailure.InvalidCode)))
        assertEquals(PairingAttemptStep.Parse, pairingStep(PairingException(PairingFailure.TlsRequired)))
        assertEquals(PairingAttemptStep.Network, pairingStep(java.net.UnknownHostException("hub.example.com"), PairingStage.Probe))
        assertEquals(PairingAttemptStep.Network, pairingStep(hub(persona.core.hub.HubError.Timeout), PairingStage.Probe))
        assertEquals(PairingAttemptStep.Tls, pairingStep(hub(persona.core.hub.HubError.CertificateMismatch), PairingStage.Probe))
        assertEquals(PairingAttemptStep.Http, pairingStep(hub(persona.core.hub.HubError.Server, 404), PairingStage.ExchangeCredentials))
        assertEquals(PairingAttemptStep.Auth, pairingStep(hub(persona.core.hub.HubError.PairingExpired, 401), PairingStage.ExchangeCredentials))
        assertEquals(PairingAttemptStep.Protocol, pairingStep(hub(persona.core.hub.HubError.Protocol), PairingStage.LoadMachines))
        assertEquals(PairingAttemptStep.Protocol, pairingStep(hub(persona.core.hub.HubError.HubMismatch), PairingStage.VerifyIdentity))
        assertEquals("Server · HTTP 404：HTTP 404", describePairingError(hub(persona.core.hub.HubError.Server, 404).let {
            persona.core.hub.HubFailure(it.kind, "HTTP 404", 404) }, "code"))
    }

    @Test fun cutShortTlsHandshakesStayOnTheNetworkStepInsteadOfSecurityCertificate() {
        val eofHandshake = javax.net.ssl.SSLHandshakeException("Remote host terminated the handshake").apply {
            initCause(java.io.EOFException("SSL peer shut down incorrectly"))
        }
        val resetHandshake = javax.net.ssl.SSLHandshakeException("Read error").apply {
            initCause(java.net.SocketException("Connection reset"))
        }
        val timeoutHandshake = javax.net.ssl.SSLHandshakeException("Handshake timed out").apply {
            initCause(java.net.SocketTimeoutException("Read timed out"))
        }
        val closedHandshake = javax.net.ssl.SSLHandshakeException("Connection closed by peer")

        for (cut in listOf(eofHandshake, resetHandshake, closedHandshake)) {
            val wrapped = PairingException(PairingFailure.Unknown).apply {
                initCause(persona.core.hub.HubFailure.from(cut))
            }
            assertEquals(cut.message, PairingAttemptStep.Network, pairingStep(wrapped, PairingStage.Probe))
            assertEquals(cut.message, PairingAttemptStep.Network, pairingStep(cut, PairingStage.Probe))
            assertEquals(cut.message, PairingFailure.Unknown, pairingFailure(cut))
        }
        val wrappedTimeout = PairingException(PairingFailure.Timeout).apply {
            initCause(persona.core.hub.HubFailure.from(timeoutHandshake))
        }
        assertEquals(PairingAttemptStep.Network, pairingStep(wrappedTimeout, PairingStage.Probe))
        assertEquals(PairingFailure.Timeout, pairingFailure(timeoutHandshake))

        val certRejected = javax.net.ssl.SSLHandshakeException("Handshake failed").apply {
            initCause(java.security.cert.CertificateException("Hub SPKI mismatch"))
        }
        val peerUnverified = javax.net.ssl.SSLPeerUnverifiedException("Hostname not verified")
        assertEquals(PairingAttemptStep.Tls, pairingStep(certRejected, PairingStage.Probe))
        assertEquals(PairingAttemptStep.Tls, pairingStep(peerUnverified, PairingStage.Probe))
        assertEquals(PairingFailure.Certificate, pairingFailure(certRejected))
        assertEquals(PairingFailure.Certificate, pairingFailure(peerUnverified))
    }

    private class LiveConnection : HubConnection, ConnectionDiagnosticsSource {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val diagnostics = MutableStateFlow(ConnectionDiagnostics())
        var reconnections = 0
        var failHistory = false
        override fun reconnect() { reconnections++; diagnostics.value = ConnectionDiagnostics() }
        override fun send(message: JsonObject) = false
        override suspend fun readDiagnostics(): ConnectionDiagnostics {
            if (failHistory) error("History unavailable")
            return diagnostics.value
        }
    }
}

@RunWith(AndroidJUnit4::class)
@org.robolectric.annotation.Config(sdk = [35])
class DiagnosticsTimeTest {
    @Test fun wireTimesShowInLocalTimeWithTheDateOnlyBeforeToday() {
        val zone = java.time.ZoneId.of("Asia/Shanghai")
        val today = java.time.LocalDate.of(2026, 10, 31)
        val zh = java.util.Locale.SIMPLIFIED_CHINESE
        assertEquals("06:03:57", localTime("2026-10-30T22:03:57.566254Z", zh, zone, today))
        assertEquals("10月30日 08:00:00", localTime("2026-10-30T08:00:00+08:00", zh, zone, today))
        assertEquals("时间未提供", localTime("时间未提供", zh, zone, today))
        assertNull(localTime(null, zh, zone, today))
    }
}
