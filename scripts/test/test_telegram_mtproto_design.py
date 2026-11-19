"""Issue #1167 design and TEST-ONLY synthetic MTProto conformance.

    python3 -B scripts/test/test_telegram_mtproto_design.py

No test here is real-account, production-transport, manager-wiring, or source-migration
acceptance. The later real manager/bridge test on an isolated hub remains a separate gate.
"""

import ast
import copy
import json
import os
from pathlib import Path
import queue
import re
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
DESIGN = ROOT / "docs/extensions/telegram-mtproto-session-transport.md"
CONTRACT = ROOT / "schema/extension/telegram-mtproto-session-transport.contract.json"
MODEL = ROOT / "scripts/test/synthetic_mtproto_model.py"
ALLOWED_CHANGES = {
    "CLAUDE.md",
    "docs/extensions/telegram-mtproto-session-transport.md",
    "schema/extension/telegram-mtproto-session-transport.contract.json",
    "scripts/test/synthetic_mtproto_model.py",
    "scripts/test/test_telegram_mtproto_design.py",
}
sys.path.insert(0, str(Path(__file__).resolve().parent))
from synthetic_mtproto_model import SyntheticError, SyntheticMTProtoModel  # noqa: E402

PART = 512 * 1024


def load_contract():
    return json.loads(CONTRACT.read_text(encoding="utf-8"))


def telegram_index_entries(text):
    # A CLAUDE.md table cell lists several documents on one line, separated by middle dots.
    design_path = DESIGN.relative_to(ROOT).as_posix()
    return sorted(entry.strip() for entry in re.split(r"[|·\n]", text) if design_path in entry)


def design_inventory_violations(root):
    """Report other files only when the branch changes this design or its shared index entry."""
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=root)

    base = git("merge-base", "HEAD", "origin/main").decode("ascii").strip()
    tracked = git("diff", "--no-renames", "--name-only", "-z", base, "--")
    untracked = git("ls-files", "--others", "--exclude-standard", "-z")
    changed = set(filter(None, os.fsdecode(tracked + untracked).split("\0")))
    outside = changed - ALLOWED_CHANGES
    if not outside or changed & (ALLOWED_CHANGES - {"CLAUDE.md"}):
        return outside
    if "CLAUDE.md" in changed:
        index = root / "CLAUDE.md"
        in_base = git("ls-tree", "--name-only", base, "--", "CLAUDE.md")
        before = git("show", f"{base}:CLAUDE.md").decode("utf-8") if in_base else ""
        after = index.read_text(encoding="utf-8") if index.is_file() else ""
        if telegram_index_entries(before) != telegram_index_entries(after):
            return outside
    return set()


def asynchronous(function, *args, **kwargs):
    """Run one synthetic call and report through a channel, never a timing sleep."""
    results = queue.Queue(maxsize=1)
    done = threading.Event()

    def run():
        try:
            results.put(("result", function(*args, **kwargs)))
        except BaseException as error:  # returned to the test thread below
            results.put(("error", error))
        finally:
            done.set()

    thread = threading.Thread(target=run, daemon=True)
    thread.start()
    return results, done, thread


def finish(call):
    results, _, thread = call
    kind, value = results.get(timeout=2)
    thread.join(timeout=2)
    if thread.is_alive():
        raise AssertionError("synthetic call did not finish after its result was delivered")
    if kind == "error":
        raise value
    return value


class DesignContractTests(unittest.TestCase):
    """AC1: maintained documentation and a machine-readable design inventory."""

    def test_design_document_covers_the_reviewed_contract(self):
        self.assertTrue(DESIGN.is_file(), f"missing maintained design document: {DESIGN.relative_to(ROOT)}")
        text = DESIGN.read_text(encoding="utf-8")
        headings = {name.strip().rstrip("#").strip()
                    for name in re.findall(r"^#{2,6}\s+(.+?)\s*$", text, re.MULTILINE)}
        required = {
            "Status and phase boundary",
            "Protocol ownership and trusted session owner",
            "Typed operations and exact 64-bit values",
            "Grants and write authorization",
            "Opaque sessions, credentials and sealed state",
            "Endpoint and proxy policy",
            "Lifecycle, cancellation and revocation",
            "RPC gate, updates and finite budgets",
            "Media and cross-DC recovery",
            "Legacy import, handoff and rollback",
            "Audit and isolation",
            "Synthetic conformance evidence",
            "Real manager/bridge isolated-hub acceptance",
        }
        self.assertEqual(set(), required - headings)
        self.assertIn("schema/extension/telegram-mtproto-session-transport.contract.json", text)
        self.assertIn("#1167", text)

    def test_contract_has_typed_operations_grants_sessions_and_finite_budgets(self):
        self.assertTrue(CONTRACT.is_file(), f"missing machine-readable contract: {CONTRACT.relative_to(ROOT)}")
        contract = load_contract()
        required = {"version", "synthetic_only", "runtime", "minimum_runtime", "protocol", "operations",
                    "grants", "scalars", "session", "secrets", "endpoint_policy", "proxy_policy", "budgets",
                    "lifecycle", "audit", "legacy_import", "forbidden_capabilities", "acceptance"}
        self.assertEqual(set(), required - set(contract))
        operations = {
            "credentials.validate", "auth.start", "auth.code", "auth.password", "auth.check", "auth.logout",
            "session.restore", "dialogs.list", "messages.read", "messages.search", "members.list",
            "updates.subscribe", "media.download", "authorization.export", "message.send", "message.forward",
            "message.react", "message.mark_read",
        }
        self.assertEqual(set(), operations - set(contract["operations"]))
        grants = set(contract["grants"])
        self.assertTrue({"read", "updates", "media", "write", "authorization_export"} <= grants)
        for name in sorted(operations):
            with self.subTest(operation=name):
                operation = contract["operations"][name]
                self.assertIs(operation["typed"], True)
                self.assertIn(operation["grant"], grants)
                self.assertIsInstance(operation["request_schema"], dict)
                self.assertIsInstance(operation["result_schema"], dict)
                self.assertGreater(operation["deadline_ms"], 0)
        session = contract["session"]
        self.assertIs(session["opaque"], True)
        self.assertIs(session["exportable"], False)
        self.assertEqual({"installation", "connector", "account", "generation", "grants"},
                         set(session["bound_to"]))
        self.assertIs(session["cross_installation_replay"], False)
        self.assertEqual("trusted_host_adapter", contract["protocol"]["owner"])
        self.assertIs(contract["protocol"]["reviewed_pinned_implementation"], True)
        for scalar in ("int64", "uint64"):
            self.assertEqual("string", contract["scalars"][scalar]["wire_type"])
            self.assertIn("decimal", contract["scalars"][scalar]["encoding"])
        budgets = contract["budgets"]
        finite = {"account_lease_seconds", "dc_lease_seconds", "pending_calls", "update_queue",
                  "media_pool_per_dc", "aggregate_host_bytes", "handshake_deadline_ms", "request_deadline_ms",
                  "reconnect_attempts", "refresh_per_burst", "legacy_import_bytes"}
        self.assertEqual(set(), finite - set(budgets))
        for name in sorted(finite):
            self.assertIsInstance(budgets[name], (int, float), name)
            self.assertNotIsInstance(budgets[name], bool, name)
            self.assertGreater(budgets[name], 0, name)
        self.assertEqual(PART, budgets["media_part_bytes"])
        self.assertEqual(500, budgets["rpc_interval_ms"]["minimum"])
        self.assertEqual(1000, budgets["rpc_interval_ms"]["maximum"])

    def test_contract_covers_secrets_isolation_lifecycle_audit_and_import(self):
        contract = load_contract()
        secrets = contract["secrets"]
        self.assertTrue({"api_hash", "authorization_key", "phone", "code", "password", "proxy_password"}
                        <= set(secrets["fields"]))
        self.assertEqual("trusted_host", secrets["submission_terminal"])
        self.assertEqual("sealed", secrets["storage"])
        self.assertIs(secrets["ordinary_rpc_events_logs_ui"], False)
        endpoints = contract["endpoint_policy"]
        self.assertEqual("verified_telegram_protocol_data", endpoints["source"])
        self.assertIs(endpoints["caller_provided_addresses"], False)
        self.assertTrue({"hub", "loopback", "metadata", "unrelated_private", "arbitrary_tcp_connect"}
                        <= set(endpoints["forbidden"]))
        self.assertIs(endpoints["authenticate_discovered_dc"], True)
        self.assertIs(endpoints["checked_address_pinning"], True)
        self.assertIs(contract["proxy_policy"]["host_owned"], True)
        self.assertIs(contract["proxy_policy"]["direct_fallback_on_failure"], False)
        self.assertTrue({"cancellation", "revocation", "logout", "generation_change", "late_event_rejection",
                         "remote_revocation_status"} <= set(contract["lifecycle"]["rules"]))
        self.assertTrue({"grant_session_lifecycle", "operation_category", "budgets", "latency", "flood_wait",
                         "redacted_outcomes"} <= set(contract["audit"]["includes"]))
        self.assertTrue({"message_content", "phone", "credentials", "authorization_keys"}
                        <= set(contract["audit"]["excludes"]))
        legacy = contract["legacy_import"]
        self.assertIs(legacy["versioned"], True)
        self.assertIs(legacy["bounded"], True)
        self.assertIs(legacy["atomic_commit"], True)
        self.assertIs(legacy["preserve_original_until_commit"], True)
        self.assertIs(legacy["rollback"], True)
        self.assertTrue({"api_credentials", "gotd_session", "account_identity", "archive_cursors", "inbox_cursors"}
                        <= set(legacy["preserves"]))


class SyntheticCase(unittest.TestCase):
    def setUp(self):
        self.model = SyntheticMTProtoModel()

    def session(self, model=None, **identity):
        return (model or self.model).new_session(**identity)

    def assertRejected(self, code, function, *args, **kwargs):
        with self.assertRaises(SyntheticError) as caught:
            function(*args, **kwargs)
        self.assertEqual(code, caught.exception.code)
        return caught.exception

    @staticmethod
    def peer(peer_id="42"):
        return {"kind": "u", "id": peer_id, "access_hash": "7"}

    @staticmethod
    def confirmation(operation, target, payload, request_id, decision="allow"):
        return {"decision": decision, "operation": operation, "target": copy.deepcopy(target),
                "payload": copy.deepcopy(payload), "request_id": request_id, "expires_at": 100}


class SyntheticAuthenticationTests(SyntheticCase):
    """AC2: synthetic authentication, recovery, DC/media behavior and exact scalars."""

    def test_credentials_phone_code_and_2fa_branches(self):
        session = self.session()
        self.assertRejected("invalid_credentials", self.model.validate_credentials,
                            session, "bad-api-id", "bad-api-hash")
        self.assertTrue(self.model.validate_credentials(session, "12345", "synthetic-api-hash")["valid"])
        login = self.model.start_login(session, "+1-555-0100")
        self.assertEqual("code_required", login["stage"])
        self.assertRejected("invalid_code", self.model.submit_code, login["login"], "000000")
        password = self.model.submit_code(login["login"], "123456")
        self.assertEqual("password_required", password["stage"])
        self.assertRejected("invalid_password", self.model.submit_password, login["login"], "wrong")
        done = self.model.submit_password(login["login"], "synthetic-2fa-password")
        self.assertEqual("done", done["stage"])
        self.assertEqual(session, done["session"])
        self.assertEqual("account-a", done["account"])
        self.assertNotIn("synthetic-2fa-password", json.dumps(done))

    def test_code_only_login_completes_without_a_2fa_stage(self):
        session = self.session()
        login = self.model.start_login(session, "+1-555-0101")
        self.assertEqual("code_required", login["stage"])
        done = self.model.submit_code(login["login"], "123456")
        self.assertEqual("done", done["stage"])
        self.assertEqual(session, done["session"])
        self.assertEqual("account-a", done["account"])
        self.assertRejected("stale_session", self.model.submit_password, login["login"], "unused")

    def test_session_recovery_distinguishes_network_failure_from_auth_expiry(self):
        original = self.session()
        state = self.model.host_state(original)
        restored = self.session(generation=2)
        self.assertEqual("restored", self.model.restore_session(restored, state)["status"])
        network = self.model.check_authorization(restored, fault="network")
        self.assertEqual("network_failure", network["status"])
        self.assertIs(network["session_retained"], True)
        self.assertIs(network["reauthentication_required"], False)
        expired = self.model.check_authorization(restored, fault="auth_expired")
        self.assertEqual("authorization_expired", expired["status"])
        self.assertIs(expired["reauthentication_required"], True)

    def test_authenticated_dc_change_updates_and_media_reference_recovery(self):
        session = self.session()
        self.model.connect(session, 2, address="149.154.167.51:443")
        moved = self.model.connect(session, 4, address="149.154.175.50:443")
        self.assertEqual(4, moved["dc"])
        self.assertIs(moved["authorization_transferred"], True)
        self.model.push_updates(session, [{"chat": "peer-1", "id": "message-1", "text": "synthetic update"}])
        self.assertEqual(1, self.model.flush_refreshes(session, limit=1)["refreshes"])
        inbox = self.model.collect_inbox(session)
        self.assertEqual(["message-1"], [item["id"] for item in inbox])
        media = self.model.download_media(session, "stable-media-ref", size=2 * PART + 1, moved=True)
        self.assertEqual([PART, PART, 1], [len(part) for part in media["parts"]])
        self.assertEqual("stable-media-ref", media["reference"])
        self.assertNotEqual("old-file-reference", media["file_reference"])

    def test_signed_and_unsigned_64_bit_values_never_become_numbers(self):
        session = self.session()
        payload = {"peer_id": str(-(1 << 63)), "access_hash": str((1 << 63) - 1),
                   "random_id": str((1 << 53) + 1), "file_id": str((1 << 64) - 1)}
        answer = self.model.request(session, "messages.read", payload)
        for name, value in payload.items():
            self.assertEqual(value, answer[name])
            self.assertIsInstance(self.model.requests[-1]["payload"][name], str)
        for name, value in (("peer_id", -(1 << 63)), ("access_hash", (1 << 63) - 1),
                            ("random_id", (1 << 53) + 1), ("file_id", (1 << 64) - 1)):
            with self.subTest(number=name):
                self.assertRejected("non_string_int64", self.model.request, session, "messages.read",
                                    dict(payload, **{name: value}))
        for name, value in (("peer_id", str(1 << 63)), ("access_hash", str(1 << 63)),
                            ("access_hash", str(-(1 << 63) - 1)), ("file_id", str(1 << 64)),
                            ("file_id", "-1")):
            with self.subTest(out_of_range=name, value=value):
                self.assertRejected("int64_out_of_range", self.model.request, session, "messages.read",
                                    dict(payload, **{name: value}))
        negative_hash = self.model.request(session, "messages.read", dict(payload, access_hash="-1"))
        self.assertEqual("-1", negative_hash["access_hash"])


class SyntheticGateTests(SyntheticCase):
    """AC3: deterministic gate, flood-wait and update-budget conformance."""

    def test_flood_wait_admits_no_request_or_connection_setup(self):
        session = self.session()
        self.model.flood_wait(session, 2)
        requests, connections = len(self.model.requests), len(self.model.route_attempts)
        self.assertRejected("flood_wait", self.model.request, session, "messages.read", {})
        self.assertRejected("flood_wait", self.model.connect, session, 4)
        self.model.advance(1)
        self.assertRejected("flood_wait", self.model.request, session, "dialogs.list", {})
        self.assertRejected("flood_wait", self.model.connect, session, 5)
        self.assertEqual(requests, len(self.model.requests))
        self.assertEqual(connections, len(self.model.route_attempts))
        self.model.advance(1)
        self.model.request(session, "messages.read", {})

    def test_authorization_export_shares_the_single_rpc_gate(self):
        session = self.session()
        first_started, release_first = threading.Event(), threading.Event()
        export_queued, export_started = threading.Event(), threading.Event()
        first = asynchronous(self.model.request, session, "messages.read", {},
                             started=first_started, release=release_first)
        self.assertTrue(first_started.wait(2), "first synthetic RPC did not start")
        export = asynchronous(self.model.request, session, "authorization.export", {},
                              queued=export_queued, started=export_started)
        try:
            self.assertTrue(export_queued.wait(2), "authorization export did not reach the gate")
            self.assertFalse(export_started.is_set(), "export bypassed the active RPC")
            release_first.set()
            self.model.advance(1)
            finish(first)
            finish(export)
        finally:
            release_first.set()
        self.assertEqual(1, self.model.metrics["max_active_rpc"])
        self.assertEqual(["messages.read", "authorization.export"], self.model.metrics["rpc_order"])
        self.assertTrue(self.model.metrics["request_gaps"])
        for gap in self.model.metrics["request_gaps"]:
            self.assertGreaterEqual(gap, 0.5)
            self.assertLessEqual(gap, 1.0)

    def test_update_refreshes_are_bounded_and_inbox_reads_cached_state(self):
        session = self.session()
        self.model.push_updates(session, [{"id": f"message-{number}"} for number in range(5)])
        progress = self.model.flush_refreshes(session, limit=2)
        self.assertEqual(2, progress["refreshes"])
        self.assertGreater(progress["pending"], 0)
        requests = len(self.model.requests)
        first = self.model.collect_inbox(session)
        second = self.model.collect_inbox(session)
        self.assertEqual(first, second)
        self.assertEqual(requests, len(self.model.requests), "Inbox collection made a remote request")

    def test_refresh_cap_and_durable_gap_survive_restart(self):
        bounded = SyntheticMTProtoModel(update_queue=200)
        session = self.session(model=bounded)
        bounded.push_updates(session, [{"id": f"message-{number}"} for number in range(150)])
        progress = bounded.flush_refreshes(session, limit=150)
        self.assertEqual(100, progress["refreshes"])
        self.assertEqual(50, progress["pending"])

        gap_session = self.session(account="account-gap")
        self.model.push_updates(gap_session, [{"id": f"message-{number}"} for number in range(8)])
        self.assertRejected("queue_exhausted", self.model.push_updates, gap_session, [{"id": "overflow"}])
        self.assertIs(self.model.public_snapshot(gap_session)["gap"], True)
        restored = self.model.restart(gap_session)
        self.assertIs(self.model.public_snapshot(restored)["gap"], True)
        reconciled = self.model.flush_refreshes(restored, limit=100)
        self.assertIs(reconciled["gap"], True)
        self.assertIs(self.model.public_snapshot(restored)["gap"], False)

    def test_slow_dc_does_not_block_unrelated_rpc_or_another_dc(self):
        session = self.session()
        slow_started, slow_release = threading.Event(), threading.Event()
        slow = asynchronous(self.model.connect, session, 4, started=slow_started, release=slow_release)
        self.assertTrue(slow_started.wait(2), "slow synthetic DC connection did not start")
        unrelated = asynchronous(self.model.request, session, "messages.read", {})
        independent = asynchronous(self.model.connect, session, 5)
        try:
            self.assertEqual("ok", finish(unrelated)["status"])
            self.assertEqual(5, finish(independent)["dc"])
            self.assertFalse(slow[1].is_set(), "slow DC finished before its deterministic release")
        finally:
            slow_release.set()
        self.assertEqual(4, finish(slow)["dc"])
    def test_revocation_and_generation_change_fence_inflight_dc_establishment(self):
        for action in ("revoke", "restart"):
            with self.subTest(action=action):
                session = self.session(account=f"account-connect-{action}")
                started, release = threading.Event(), threading.Event()
                pending = asynchronous(self.model.connect, session, 4, started=started, release=release)
                self.assertTrue(started.wait(2))
                try:
                    if action == "revoke":
                        self.model.revoke(session)
                    else:
                        self.model.restart(session)
                    with self.assertRaises(SyntheticError) as caught:
                        finish(pending)
                    self.assertEqual("revoked" if action == "revoke" else "stale_generation",
                                     caught.exception.code)
                    self.assertEqual([], self.model.connections)
                finally:
                    release.set()


class SyntheticWriteLifecycleTests(SyntheticCase):
    """AC4: confirmed single writes and terminal lifecycle behavior."""

    def test_confirmation_rejection_causes_zero_write_requests(self):
        session = self.session()
        target, payload = self.peer("-1001234567890"), {"text": "synthetic write"}
        confirmation = self.confirmation("message.send", target, payload, "write-denied", decision="deny")
        self.assertRejected("confirmation_rejected", self.model.write, session, "message.send", target, payload,
                            request_id="write-denied", confirmation=confirmation)
        self.assertEqual([], self.model.requests)

    def test_write_rejects_numeric_peer_and_access_hash_values_before_sending(self):
        session = self.session()
        for field in ("id", "access_hash"):
            with self.subTest(field=field):
                target = self.peer()
                target[field] = (1 << 53) + 1
                payload, request_id = {"text": "exact integers"}, f"write-number-{field}"
                confirmation = self.confirmation("message.send", target, payload, request_id)
                self.assertRejected("non_string_int64", self.model.write, session, "message.send",
                                    target, payload, request_id=request_id, confirmation=confirmation)
        self.assertEqual([], self.model.requests)

    def test_duplicate_and_ambiguous_writes_never_send_twice(self):
        for fault in (None, "drop_after_commit"):
            with self.subTest(fault=fault):
                session = self.session(account=f"account-{fault}")
                target, payload, request_id = self.peer(), {"text": "once"}, f"write-{fault}"
                confirmation = self.confirmation("message.send", target, payload, request_id)
                first = self.model.write(session, "message.send", target, payload, request_id=request_id,
                                         confirmation=confirmation, fault=fault)
                second = self.model.write(session, "message.send", target, payload, request_id=request_id,
                                          confirmation=confirmation, fault=fault)
                self.assertEqual(first, second)
                self.assertIsInstance(first["random_id"], str)
                self.assertGreater(int(first["random_id"]), 1 << 53)
                self.assertEqual("unknown" if fault else "confirmed", first["status"])
                sent = [request for request in self.model.requests if request.get("request_id") == request_id]
                self.assertEqual(1, len(sent))
        self.assertEqual(0, self.model.metrics.get("automatic_write_retries", 0))

    def test_cancellation_releases_pending_work_and_preserves_unknown_outcome(self):
        session = self.session()
        target, payload, request_id = self.peer(), {"text": "cancel"}, "write-cancel"
        confirmation = self.confirmation("message.send", target, payload, request_id)
        started, release = threading.Event(), threading.Event()
        pending = asynchronous(self.model.write, session, "message.send", target, payload,
                               request_id=request_id, confirmation=confirmation, fault="drop_after_commit",
                               started=started, release=release)
        self.assertTrue(started.wait(2), "synthetic write did not start")
        try:
            self.model.cancel(session, request_id)
            outcome = finish(pending)  # cancellation must not wait for release
            self.assertEqual("unknown", outcome["status"])
            self.assertEqual(1, len([request for request in self.model.requests
                                     if request.get("request_id") == request_id]))
        finally:
            release.set()

    def test_revocation_logout_and_stale_generations_cannot_resurrect_sessions(self):
        for action in ("revoke", "logout"):
            with self.subTest(action=action):
                session = self.session(account=f"account-{action}")
                state = self.model.host_state(session)
                login = self.model.start_login(session, "+1-555-0100")
                outcome = getattr(self.model, action)(session, remote_available=False)
                self.assertIs(outcome["local_transport_closed"], True)
                self.assertIs(outcome["remote_session_revoked"], False)
                self.assertRejected("stale_session", self.model.submit_code, login["login"], "123456")
                self.assertRejected("stale_session", self.model.restore_session, session, state)
                status = self.model.session_status(session)
                self.assertIs(status["authorized"], False)
                self.assertEqual("closed", status["local_transport"])
        old = self.session(account="account-generations", generation=1)
        current = self.session(account="account-generations", generation=2)
        self.assertRejected("stale_generation", self.model.request, old, "messages.read", {})
        self.assertRejected("stale_generation", self.model.push_updates, old, [{"id": "late"}], generation=1)
        self.assertEqual("ok", self.model.request(current, "messages.read", {})["status"])


class SyntheticCompatibilityTests(SyntheticCase):
    """AC5: synthetic legacy import, atomic rollback and restart-safe progress."""

    def test_legacy_import_is_bounded_atomic_and_rollback_safe(self):
        session = self.session(account="2123456789")
        before = self.model.host_state(session)
        legacy = {"version": 1, "api_id": "12345", "api_hash": "synthetic-api-hash",
                  "gotd_session": "synthetic-gotd-session", "account_id": "2123456789",
                  "cursors": {"archive": "archive-cursor", "inbox": "inbox-cursor"}}
        imported = self.model.import_legacy(session, legacy)
        self.assertEqual("2123456789", imported["account_id"])
        self.assertEqual(legacy["cursors"], imported["cursors"])
        self.assertNotIn("synthetic-api-hash", json.dumps(imported))
        after = self.model.host_state(session)
        self.assertNotEqual(before, after)
        cases = []
        for kind in ("corrupt", "unsupported", "oversized"):
            blob = copy.deepcopy(legacy)
            if kind == "corrupt":
                blob["gotd_session"] = "not-a-session"
            elif kind == "unsupported":
                blob["version"] = 999
            else:
                blob["gotd_session"] = "x" * (8 * 1024 * 1024 + 1)
            cases.append((kind, f"legacy_{kind}" if kind != "corrupt" else "legacy_corrupt", blob, None))
        cases += [("unavailable", "legacy_unavailable", legacy, "unavailable"),
                  ("commit", "import_commit_failed", legacy, "commit")]
        cases.append(("missing_credentials", "legacy_corrupt",
                      {"version": 1, "gotd_session": "synthetic-gotd-session"}, None))
        mismatched = copy.deepcopy(legacy)
        mismatched["account_id"] = "2123456790"
        cases.append(("mismatched_account", "cross_account", mismatched, None))
        for name, code, blob, fault in cases:
            with self.subTest(failure=name):
                self.assertRejected(code, self.model.import_legacy, session, blob, fault=fault)
                self.assertEqual(after, self.model.host_state(session), "failed import changed sealed state")
        self.model.rollback_import(session, imported["import_id"])
        self.assertEqual(before, self.model.host_state(session))

    def test_partial_archive_cursor_recovers_without_duplicates_or_inbox_loss(self):
        session = self.session()
        chat = {"id": "chat-a", "inbox_cursor": "inbox-7"}
        first_page = [{"id": "message-1"}, {"id": "message-2"}]
        second_page = [{"id": "message-3"}]
        partial = self.model.archive_sync(session, chat, [first_page, second_page], fail_after=1)
        self.assertEqual("partial", partial["status"])
        self.assertTrue(partial["cursor"])
        self.assertEqual(["message-1", "message-2"], [message["id"] for message in partial["messages"]])
        restored = self.model.restart(session)
        done = self.model.archive_sync(restored, chat, [second_page], cursor=partial["cursor"])
        identifiers = [message["id"] for message in partial["messages"] + done["messages"]]
        self.assertEqual(["message-1", "message-2", "message-3"], identifiers)
        self.assertEqual("inbox-7", done["inbox_cursor"])

    def test_media_reference_and_resume_token_survive_restart(self):
        session = self.session()
        first = self.model.download_media(session, "restart-safe-ref", size=2 * PART + 1,
                                          moved=True, max_parts=1)
        self.assertEqual([PART], [len(part) for part in first["parts"]])
        self.assertTrue(first["resume"])
        restored = self.model.restart(session)
        rest = self.model.download_media(restored, "restart-safe-ref", size=2 * PART + 1,
                                         moved=True, resume=first["resume"])
        self.assertEqual("restart-safe-ref", rest["reference"])
        self.assertEqual([PART, 1], [len(part) for part in rest["parts"]])
        self.assertEqual(2 * PART + 1, sum(len(part) for part in first["parts"] + rest["parts"]))
        self.assertNotEqual(first["file_reference"], rest["file_reference"])


class SyntheticIsolationTests(SyntheticCase):
    """AC6: endpoint, ownership, resource and redaction isolation."""

    def test_forbidden_and_unauthenticated_endpoints_are_rejected(self):
        session = self.session()
        forbidden = ("127.0.0.1:443", "[::1]:443", "<metadata-address>:80", "<private-address>:443",
                     "hub.invalid:443", "example.com:443")
        for address in forbidden:
            with self.subTest(address=address):
                self.assertRejected("forbidden_endpoint", self.model.connect, session, 2, address=address)
        self.assertRejected("caller_endpoint", self.model.connect, session, 2,
                            address="149.154.167.51:443", endpoint_source="guest")
        self.assertRejected("unauthenticated_dc", self.model.connect, session, 4,
                            address="149.154.175.50:443", authenticated=False)
        self.assertEqual(2, self.model.connect(session, 2, address="149.154.167.51:443")["dc"])

    def test_proxy_failure_never_falls_back_to_direct_networking(self):
        session = self.session()
        self.model.configure_proxy(session, address="proxy.invalid:1080",
                                   username="synthetic-proxy-user", password="synthetic-proxy-password")
        attempts = len(self.model.route_attempts)
        self.assertRejected("proxy_failure", self.model.connect, session, 2,
                            address="149.154.167.51:443", proxy="host", fault="proxy_failure")
        attempted = self.model.route_attempts[attempts:]
        self.assertTrue(attempted)
        self.assertEqual({"proxy"}, {attempt["route"] for attempt in attempted})

    def test_cross_account_and_cross_extension_handles_are_rejected(self):
        session = self.session()
        self.session(account="account-b")
        neighbor = self.session(connector="neighbor", account="neighbor-a")
        self.assertRejected("cross_account", self.model.request, session, "messages.read", {},
                            caller=("telegram", "account-b"))
        self.assertRejected("cross_extension", self.model.request, session, "messages.read", {},
                            caller=("neighbor", "account-a"))
        self.assertEqual("ok", self.model.neighbor_call(neighbor)["status"])

    def test_sealed_state_and_resume_tokens_enforce_all_identity_boundaries(self):
        session = self.session()
        sealed = self.model.host_state(session)
        neighbor = self.session(connector="neighbor", account="account-a")
        other_installation = self.session(installation="installation-b")
        other_account = self.session(account="account-b")
        self.assertRejected("cross_extension", self.model.restore_session, neighbor, sealed)
        self.assertRejected("cross_installation", self.model.restore_session, other_installation, sealed)
        self.assertRejected("cross_account", self.model.restore_session, other_account, sealed)

        media = self.model.download_media(session, "identity-bound-media", size=PART + 1, max_parts=1)
        for target, code in ((neighbor, "cross_extension"), (other_installation, "cross_installation"),
                             (other_account, "cross_account")):
            with self.subTest(boundary=code):
                self.assertRejected(code, self.model.download_media, target, "identity-bound-media",
                                    size=PART + 1, resume=media["resume"])

    def test_stale_events_cannot_mutate_a_restarted_account(self):
        session = self.session()
        current = self.model.restart(session)
        before = self.model.collect_inbox(current)
        self.assertRejected("stale_generation", self.model.push_updates, session, [{"id": "old-handle"}])
        self.assertRejected("stale_generation", self.model.push_updates, current, [{"id": "old-event"}], generation=1)
        self.assertEqual(before, self.model.collect_inbox(current))

    def test_queue_and_byte_exhaustion_do_not_affect_a_neighbor(self):
        limited = SyntheticMTProtoModel(update_queue=1, byte_budget=16)
        session = self.session(model=limited)
        limited.push_updates(session, [{"id": "one"}])
        self.assertRejected("queue_exhausted", limited.push_updates, session, [{"id": "two"}])
        self.assertLessEqual(len(limited.updates), 1)
        self.assertRejected("byte_budget_exceeded", limited.download_media, session, "bounded-media", size=17)
        self.assertLessEqual(limited.metrics.get("media_bytes", 0), 16)
        neighbor = self.session(model=limited, connector="neighbor", account="neighbor-a")
        self.assertEqual("ok", limited.neighbor_call(neighbor)["status"])
        limited.download_media(session, "account-budget-media", size=16)
        neighboring_media = limited.download_media(neighbor, "neighbor-budget-media", size=1)
        self.assertEqual([1], [len(part) for part in neighboring_media["parts"]])

    def test_credentials_and_content_are_redacted_from_observable_surfaces(self):
        session = self.session()
        secrets = ("synthetic-api-hash", "+1-555-0100", "123456", "synthetic-2fa-password",
                   "654321", "synthetic-proxy-password")
        errors = [str(self.assertRejected("invalid_credentials", self.model.validate_credentials,
                                         session, "invalid-api-id", secrets[0]))]
        login = self.model.start_login(session, secrets[1])
        errors.append(str(self.assertRejected("invalid_code", self.model.submit_code, login["login"], "000000")))
        self.model.submit_code(login["login"], secrets[4])
        errors.append(str(self.assertRejected("invalid_password", self.model.submit_password,
                                              login["login"], "wrong")))
        self.model.submit_password(login["login"], secrets[3])
        self.model.configure_proxy(session, address="proxy.invalid:1080", username="proxy-user", password=secrets[5])
        errors.append(str(self.assertRejected("proxy_failure", self.model.connect, session, 2,
                                              address="149.154.167.51:443", proxy="host", fault="proxy_failure")))
        self.model.push_updates(session, [{"id": "private-message", "text": "synthetic-private-message-content"}])
        credential_surfaces = json.dumps({"snapshot": self.model.public_snapshot(session), "audit": self.model.audit,
                                          "errors": self.model.errors, "events": self.model.events,
                                          "sealed": self.model.host_state(session)}, default=str) + "".join(errors)
        for secret in secrets:
            self.assertNotIn(secret, credential_surfaces)
        content_surfaces = json.dumps({"audit": self.model.audit, "errors": self.model.errors,
                                       "events": self.model.events}, default=str) + "".join(errors)
        self.assertNotIn("synthetic-private-message-content", content_surfaces)


class PhaseBoundaryTests(SyntheticCase):
    """AC7: design-only changes, negative capabilities and synthetic-only evidence."""

    def test_changed_files_stay_within_the_design_phase_inventory(self):
        self.assertEqual(set(), design_inventory_violations(ROOT))

    def test_reference_model_has_no_real_network_or_process_capability(self):
        tree = ast.parse(MODEL.read_text(encoding="utf-8"), filename=str(MODEL))
        imported = set()
        dynamic_imports = []
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                imported.update(alias.name.split(".")[0] for alias in node.names)
            elif isinstance(node, ast.ImportFrom) and node.module:
                imported.add(node.module.split(".")[0])
            elif isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "__import__":
                dynamic_imports.append(node.lineno)
        forbidden = {"socket", "ssl", "http", "urllib", "requests", "aiohttp", "asyncio", "subprocess",
                     "ctypes", "importlib", "telethon", "pyrogram"}
        self.assertEqual(set(), imported & forbidden)
        self.assertEqual([], dynamic_imports)

    def test_raw_tl_socket_connect_and_runtime_enablement_are_rejected(self):
        session = self.session()
        for operation in ("raw.invoke", "tl.invoke", "tcp.connect", "http.connect", "tunnel.open"):
            with self.subTest(operation=operation):
                self.assertRejected("unsupported_operation", self.model.request, session, operation, {})
        self.assertEqual([], self.model.requests)
        evidence = self.model.evidence()
        self.assertIs(evidence["synthetic"], True)
        self.assertIs(evidence["real_account_access"], False)
        self.assertIs(evidence["runtime_enabled"], False)
        self.assertIs(evidence["source_migration_enabled"], False)

    def test_document_and_contract_retain_the_later_real_acceptance_gate(self):
        self.assertTrue(DESIGN.is_file())
        text = DESIGN.read_text(encoding="utf-8").lower()
        for phrase in ("synthetic", "real manager/bridge", "isolated hub", "separate explicit approval",
                       "sanitized evidence", "source migration", "blocked", "minimum runtime"):
            self.assertIn(phrase, text)
        contract = load_contract()
        self.assertIs(contract["synthetic_only"], True)
        runtime = contract["runtime"]
        self.assertIs(runtime["enabled"], False)
        self.assertIs(runtime["production_transport"], False)
        self.assertIs(runtime["real_account_access"], False)
        self.assertEqual("blocked", runtime["source_migration"])
        self.assertTrue({"raw_tl_invocation", "arbitrary_methods", "raw_socket", "guest_connect", "guest_tunnel"}
                        <= set(contract["forbidden_capabilities"]))
        acceptance = contract["acceptance"]
        self.assertEqual("required", acceptance["real_manager_bridge_isolated_hub"])
        self.assertEqual("separate_explicit_approval", acceptance["real_account_tests"])
        self.assertEqual("sanitized", acceptance["real_account_evidence"])
        self.assertIs(acceptance["synthetic_results_are_not_real_acceptance"], True)


class DesignInventoryScopeTests(unittest.TestCase):
    outside = "server/fixture.txt"
    entry = f"`{DESIGN.relative_to(ROOT).as_posix()}` synthetic design"
    index = f"| Extensions | `other.md` neighbor · {entry} |\n"

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        isolated_git = patch.dict(os.environ, env, clear=True)
        isolated_git.start()
        self.addCleanup(isolated_git.stop)
        self.git("init", "-q")
        for name in ALLOWED_CHANGES - {"CLAUDE.md"}:
            self.write(name, "original\n")
        self.write("CLAUDE.md", self.index)
        self.write(self.outside, "original\n")
        self.git("add", ".")
        self.git("commit", "-qm", "fixture base")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")

    def git(self, *args):
        subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", *args],
                       cwd=self.root, check=True, capture_output=True)

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def test_clean_and_unrelated_git_states_pass(self):
        self.assertEqual(set(), design_inventory_violations(self.root), "clean")
        self.write("new.txt", "untracked")
        self.assertEqual(set(), design_inventory_violations(self.root), "untracked")
        (self.root / "new.txt").unlink()
        self.write(self.outside, "edited")
        self.assertEqual(set(), design_inventory_violations(self.root), "unstaged")
        self.git("add", self.outside)
        self.assertEqual(set(), design_inventory_violations(self.root), "staged")
        self.git("commit", "-qm", "unrelated work")
        self.assertEqual(set(), design_inventory_violations(self.root), "committed")

    def test_each_dedicated_file_activates_the_inventory(self):
        for name in sorted(ALLOWED_CHANGES - {"CLAUDE.md"}):
            with self.subTest(name=name):
                self.write(name, "design change")
                self.assertEqual(set(), design_inventory_violations(self.root))
                self.write(self.outside, "outside change")
                self.assertEqual({self.outside}, design_inventory_violations(self.root))
                self.git("restore", "--", self.outside, name)

    def test_shared_index_compares_the_telegram_entry(self):
        self.write(self.outside, "outside change")
        self.write("CLAUDE.md", self.index.replace("neighbor", "updated neighbor"))
        self.assertEqual(set(), design_inventory_violations(self.root))
        for entry in ("", self.entry + " revised", f"{self.entry} · {self.entry}"):
            with self.subTest(entry=entry):
                self.write("CLAUDE.md", self.index.replace(self.entry, entry))
                self.assertEqual({self.outside}, design_inventory_violations(self.root))

    def test_committed_design_reports_an_untracked_path(self):
        self.write(CONTRACT.relative_to(ROOT), "design change")
        self.git("commit", "-qam", "design work")
        outside = "untracked outside.txt"
        self.write(outside, "outside change")
        self.assertEqual({outside}, design_inventory_violations(self.root))

    def test_moving_design_outside_reports_the_destination(self):
        self.git("mv", MODEL.relative_to(ROOT).as_posix(), "moved.py")
        self.assertEqual({"moved.py"}, design_inventory_violations(self.root))


if __name__ == "__main__":
    unittest.main()
