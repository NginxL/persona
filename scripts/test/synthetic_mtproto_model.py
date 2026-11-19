"""TEST-ONLY synthetic reference model for the controlled MTProto design.

This module has no network, process, filesystem, credential-store, or Telegram implementation. It makes the design's state transitions executable with fake time and deterministic threading.Event hooks. It is not a production API or evidence of real-account access.
"""

import copy
import json
import re
import threading


class SyntheticError(Exception):
    """A redacted synthetic refusal with a stable machine-readable code."""

    def __init__(self, code, message=""):
        super().__init__(message or code)
        self.code = code


class SyntheticMTProtoModel:
    """Executable design model; all transport and upstream behavior is synthetic."""

    def __init__(self, *, update_queue=8, byte_budget=4 * 1024 * 1024):
        self.update_queue_limit = update_queue
        self.byte_budget = byte_budget
        self.requests = []
        self.connections = []
        self.route_attempts = []
        self.updates = []
        self.events = []
        self.audit = []
        self.errors = []
        self.metrics = {"max_active_rpc": 0, "rpc_order": [], "request_gaps": [],
                        "automatic_write_retries": 0, "media_bytes": 0}
        self._condition = threading.Condition(threading.RLock())
        self._sessions = {}
        self._current = {}
        self._logins = {}
        self._sealed = {}
        self._imports = {}
        self._resume_tokens = {}
        self._time = 0.0
        self._counter = 0
        self._file_counter = 0
        self._random_counter = 0

    def _next(self, kind):
        with self._condition:
            self._counter += 1
            return f"synthetic-{kind}-{self._counter}"

    def _reject(self, code):
        with self._condition:
            self.errors.append(code)
        raise SyntheticError(code)

    def _audit(self, action, operation=None):
        record = {"action": action, "at": self._time}
        if operation is not None:
            record["operation"] = operation
        self.audit.append(record)

    @staticmethod
    def _identity(installation, connector, account):
        return installation, connector, account

    def _resolve(self, session):
        with self._condition:
            state = self._sessions.get(session)
            if state is None:
                self._reject("stale_session")
            key = self._identity(state["installation"], state["connector"], state["account"])
            if self._current.get(key) != state["generation"]:
                self._reject("stale_generation")
            return state

    @staticmethod
    def _new_host_state(sealed, installation, connector, account):
        return {"sealed": sealed, "installation": installation, "connector": connector,
                "account_id": account, "cursors": {"archive": "", "inbox": ""}}

    def new_session(self, *, installation="installation-a", connector="telegram",
                    account="account-a", generation=1):
        with self._condition:
            session = self._next("account-handle")
            sealed = self._next("sealed")
            key = self._identity(installation, connector, account)
            previous_generation = self._current.get(key)
            if previous_generation is not None:
                for previous in self._sessions.values():
                    if self._identity(previous["installation"], previous["connector"], previous["account"]) == key \
                            and previous["generation"] == previous_generation:
                        previous["generation_closed"] = True
                        previous["local_transport"] = "closed"
                        self._condition.notify_all()
            state = {
                "session": session,
                "installation": installation,
                "connector": connector,
                "account": account,
                "generation": generation,
                "host": self._new_host_state(sealed, installation, connector, account),
                "authorized": True,
                "revoked": False,
                "generation_closed": False,
                "local_transport": "open",
                "remote_session_revoked": False,
                "flood_until": 0.0,
                "gate": {"active": 0, "last_completed": None},
                "pending": [],
                "gap": False,
                "inbox": [],
                "archive": {},
                "media": {},
                "media_bytes": 0,
                "journal": {},
                "proxy": None,
            }
            self._sealed[sealed] = {"account": account, "authorization": "synthetic-authorization"}
            self._sessions[session] = state
            self._current[self._identity(installation, connector, account)] = generation
            self._audit("session_created")
            return session

    def validate_credentials(self, session, api_id, api_hash):
        state = self._resolve(session)
        if api_id != "12345" or api_hash != "synthetic-api-hash":
            self._reject("invalid_credentials")
        sealed = state["host"]["sealed"]
        self._sealed[sealed].update(api_id=api_id, api_hash=api_hash)
        self._audit("credentials_validated", "credentials.validate")
        return {"valid": True}

    def start_login(self, session, phone):
        state = self._resolve(session)
        if state["revoked"]:
            self._reject("stale_session")
        login = self._next("login-flow")
        self._logins[login] = {"session": session, "phone": phone, "stage": "code_required",
                               "attempts": 0, "active": True}
        self.events.append({"kind": "login_stage", "stage": "code_required"})
        return {"login": login, "stage": "code_required"}

    def _login(self, login):
        flow = self._logins.get(login)
        if flow is None or not flow["active"]:
            self._reject("stale_session")
        state = self._resolve(flow["session"])
        if state["revoked"]:
            self._reject("stale_session")
        return flow, state

    def submit_code(self, login, code):
        flow, state = self._login(login)
        if flow["stage"] != "code_required":
            self._reject("stale_session")
        if code not in {"123456", "654321"}:
            flow["attempts"] += 1
            if flow["attempts"] >= 5:
                flow["active"] = False
            self._reject("invalid_code")
        if flow["phone"] == "+1-555-0101":
            flow["stage"] = "done"
            flow["active"] = False
            state["authorized"] = True
            self._audit("login_completed", "auth.code")
            return {"login": login, "stage": "done", "session": state["session"],
                    "account": state["account"]}
        flow["stage"] = "password_required"
        self.events.append({"kind": "login_stage", "stage": "password_required"})
        return {"login": login, "stage": "password_required"}

    def submit_password(self, login, password):
        flow, state = self._login(login)
        if flow["stage"] != "password_required":
            self._reject("stale_session")
        if password != "synthetic-2fa-password":
            flow["attempts"] += 1
            if flow["attempts"] >= 5:
                flow["active"] = False
            self._reject("invalid_password")
        flow["stage"] = "done"
        flow["active"] = False
        state["authorized"] = True
        self._audit("login_completed", "auth.password")
        return {"stage": "done", "session": state["session"], "account": state["account"]}

    def host_state(self, session):
        state = self._resolve(session)
        with self._condition:
            return copy.deepcopy(state["host"])

    def restore_session(self, session, state):
        current = self._resolve(session)
        if current["revoked"]:
            self._reject("stale_session")
        if not isinstance(state, dict) or state.get("sealed") not in self._sealed:
            self._reject("legacy_corrupt")
        if state.get("connector") != current["connector"]:
            self._reject("cross_extension")
        if state.get("installation") != current["installation"]:
            self._reject("cross_installation")
        if state.get("account_id") != current["account"]:
            self._reject("cross_account")
        with self._condition:
            current["host"] = copy.deepcopy(state)
            current["authorized"] = True
            current["local_transport"] = "open"
        self._audit("session_restored", "session.restore")
        return {"status": "restored"}

    def restart(self, session):
        old = self._resolve(session)
        if old["revoked"]:
            self._reject("revoked")
        current = self.new_session(installation=old["installation"], connector=old["connector"],
                                   account=old["account"], generation=old["generation"] + 1)
        new = self._sessions[current]
        with self._condition:
            for key in ("host", "authorized", "flood_until", "pending", "gap", "inbox", "archive",
                        "media", "media_bytes", "journal", "proxy"):
                new[key] = copy.deepcopy(old[key])
            new["gate"]["last_completed"] = old["gate"]["last_completed"]
        self._audit("generation_started")
        return current

    def check_authorization(self, session, *, fault=None):
        state = self._resolve(session)
        if fault == "network":
            return {"status": "network_failure", "session_retained": True,
                    "reauthentication_required": False}
        if fault == "auth_expired":
            state["authorized"] = False
            return {"status": "authorization_expired", "session_retained": True,
                    "reauthentication_required": True}
        self._audit("authorization_checked", "auth.check")
        return {"status": "authorized", "session_retained": True,
                "reauthentication_required": False}

    def session_status(self, session):
        state = self._resolve(session)
        return {"authorized": state["authorized"] and not state["revoked"],
                "local_transport": state["local_transport"],
                "remote_session_revoked": state["remote_session_revoked"]}

    def logout(self, session, *, remote_available=True):
        return self.revoke(session, remote_available=remote_available)

    def revoke(self, session, *, remote_available=True):
        state = self._resolve(session)
        with self._condition:
            state["revoked"] = True
            state["authorized"] = False
            state["local_transport"] = "closed"
            state["remote_session_revoked"] = bool(remote_available)
            state["pending"].clear()
            self._condition.notify_all()
            for flow in self._logins.values():
                if flow["session"] == session:
                    flow["active"] = False
            self._audit("local_revocation", "auth.logout")
            self._audit("remote_revocation", "auth.logout")
        return {"local_transport_closed": True,
                "remote_session_revoked": bool(remote_available)}

    @staticmethod
    def _validate_scalar(name, value):
        if not isinstance(value, str):
            raise SyntheticError("non_string_int64")
        if re.fullmatch(r"-?(0|[1-9][0-9]*)", value) is None:
            raise SyntheticError("int64_out_of_range")
        number = int(value)
        if name == "file_id":
            low, high = 0, (1 << 64) - 1
        else:
            low, high = -(1 << 63), (1 << 63) - 1
        if not low <= number <= high:
            raise SyntheticError("int64_out_of_range")

    def _validate_payload(self, payload):
        for name in ("peer_id", "access_hash", "random_id", "file_id"):
            if name in payload:
                try:
                    self._validate_scalar(name, payload[name])
                except SyntheticError as error:
                    self._reject(error.code)

    def _check_caller(self, state, caller):
        if caller is None:
            return
        connector, account = caller
        if connector != state["connector"]:
            self._reject("cross_extension")
        if account != state["account"]:
            self._reject("cross_account")

    def _check_flood(self, state):
        if self._time < state["flood_until"]:
            self._reject("flood_wait")

    def _enter_rpc(self, state, operation, payload, request_id, queued, started):
        with self._condition:
            self._check_flood(state)
            if queued is not None:
                queued.set()
            gate = state["gate"]
            while gate["active"]:
                self._condition.wait()
                self._resolve(state["session"])
                self._check_flood(state)
            if state["revoked"]:
                self._reject("revoked")
            last = gate["last_completed"]
            if last is not None:
                ready = last + 0.5
                if self._time < ready:
                    self._time = ready
                self.metrics["request_gaps"].append(self._time - last)
            gate["active"] = 1
            self.metrics["max_active_rpc"] = max(self.metrics["max_active_rpc"], gate["active"])
            self.metrics["rpc_order"].append(operation)
            self.requests.append({"operation": operation, "payload": copy.deepcopy(payload),
                                  "request_id": request_id})
            self._audit("rpc_started", operation)
        if started is not None:
            started.set()

    def _exit_rpc(self, state):
        with self._condition:
            state["gate"]["active"] = 0
            state["gate"]["last_completed"] = self._time
            self._condition.notify_all()

    def request(self, session, operation, payload=None, *, caller=None, request_id=None,
                queued=None, started=None, release=None, fault=None):
        state = self._resolve(session)
        self._check_caller(state, caller)
        allowed = {"credentials.validate", "auth.start", "auth.code", "auth.password", "auth.check",
                   "auth.logout", "session.restore", "dialogs.list", "messages.read", "messages.get",
                   "messages.search", "members.list", "updates.subscribe", "updates.refresh", "media.download",
                   "authorization.export", "message.send", "message.forward", "message.react",
                   "message.mark_read", "inbox.collect"}
        if operation not in allowed:
            self._reject("unsupported_operation")
        if state["revoked"]:
            self._reject("revoked")
        payload = {} if payload is None else dict(payload)
        self._validate_payload(payload)
        request_id = request_id or self._next("request")
        self._enter_rpc(state, operation, payload, request_id, queued, started)
        try:
            if release is not None:
                release.wait()
            if fault == "network":
                return {"status": "network_failure"}
            answer = copy.deepcopy(payload)
            answer["status"] = "ok"
            if operation == "authorization.export":
                answer["authorization"] = "redacted"
            return answer
        finally:
            self._exit_rpc(state)

    def connect(self, session, dc, *, address=None, authenticated=True,
                endpoint_source="host", proxy=None, started=None, release=None, fault=None):
        state = self._resolve(session)
        with self._condition:
            self._check_flood(state)
        if state["revoked"]:
            self._reject("revoked")
        if endpoint_source == "guest":
            self._reject("caller_endpoint")
        endpoints = {2: "149.154.167.51:443", 4: "149.154.175.50:443", 5: "149.154.175.51:443"}
        address = address or endpoints.get(dc)
        if endpoints.get(dc) != address:
            self._reject("forbidden_endpoint")
        if not authenticated:
            self._reject("unauthenticated_dc")
        route = "proxy" if proxy == "host" or state["proxy"] is not None else "direct"
        self.route_attempts.append({"dc": dc, "route": route})
        if route == "proxy" and fault == "proxy_failure":
            self._reject("proxy_failure")
        if started is not None:
            started.set()
        outcome = {"release_ready": False}
        if release is not None:
            def released():
                release.wait()
                with self._condition:
                    outcome["release_ready"] = True
                    self._condition.notify_all()
            threading.Thread(target=released, daemon=True).start()
        with self._condition:
            while release is not None and not outcome["release_ready"] \
                    and not state["revoked"] and not state["generation_closed"]:
                self._condition.wait()
            if state["revoked"]:
                self._reject("revoked")
            self._resolve(session)
            self._check_flood(state)
            self.connections.append({"dc": dc, "route": route})
            self._audit("dc_connected")
        return {"dc": dc, "authorization_transferred": True, "route": route}

    def configure_proxy(self, session, *, address, username, password):
        state = self._resolve(session)
        state["proxy"] = {"address": address, "username": username, "password": password}
        self._audit("proxy_configured")

    def flood_wait(self, session, seconds):
        state = self._resolve(session)
        with self._condition:
            state["flood_until"] = max(state["flood_until"], self._time + seconds)
            self._audit("flood_wait")
            self._condition.notify_all()

    def advance(self, seconds):
        with self._condition:
            self._time += seconds
            self._condition.notify_all()

    def push_updates(self, session, updates, *, generation=None):
        state = self._resolve(session)
        if generation is not None and generation != state["generation"]:
            self._reject("stale_generation")
        if state["revoked"]:
            self._reject("revoked")
        if len(state["pending"]) + len(updates) > self.update_queue_limit:
            state["gap"] = True
            self.events.append({"kind": "gap"})
            self._audit("update_gap", "updates.subscribe")
            self._reject("queue_exhausted")
        copied = copy.deepcopy(updates)
        state["pending"].extend(copied)
        self.updates.extend(copied)
        self.events.append({"kind": "updates", "count": len(copied)})
        self._audit("updates_received", "updates.subscribe")

    @staticmethod
    def _inbox_eligible(update):
        return not any(update.get(flag) for flag in ("bot", "self", "broadcast", "muted", "archived"))

    def flush_refreshes(self, session, *, limit):
        state = self._resolve(session)
        if limit < 1:
            self._reject("budget_exhausted")
        count = min(limit, 100, len(state["pending"]))
        had_gap = state["gap"]
        if count:
            self.request(session, "updates.refresh", {"count": count})
            batch = state["pending"][:count]
            del state["pending"][:count]
            for update in batch:
                if not self._inbox_eligible(update):
                    continue
                state["inbox"] = [old for old in state["inbox"] if old.get("id") != update.get("id")]
                state["inbox"].append(update)
        self._audit("refresh_round", "updates.refresh")
        if had_gap and not state["pending"]:
            state["gap"] = False
        return {"refreshes": count, "pending": len(state["pending"]), "gap": had_gap}

    def collect_inbox(self, session):
        state = self._resolve(session)
        return copy.deepcopy(state["inbox"])

    @staticmethod
    def _fingerprint(operation, target, payload):
        return json.dumps({"operation": operation, "target": target, "payload": payload},
                          sort_keys=True, separators=(",", ":"))

    def _validate_peer(self, peer):
        if not isinstance(peer, dict) or peer.get("kind") not in {"u", "g", "c"} \
                or "id" not in peer or "access_hash" not in peer:
            self._reject("invalid_write")
        for name, field in (("peer_id", "id"), ("access_hash", "access_hash")):
            try:
                self._validate_scalar(name, peer[field])
            except SyntheticError as error:
                self._reject(error.code)

    def _random_ids(self, count):
        values = []
        for _ in range(count):
            self._random_counter += 1
            values.append(str((1 << 53) + self._random_counter))
        return values

    @staticmethod
    def _receipt(entry, status):
        receipt = {"status": status, "request_id": entry["request_id"]}
        if entry["operation"] == "message.send":
            receipt["random_id"] = entry["random_ids"][0]
        elif entry["operation"] == "message.forward":
            receipt["random_ids"] = list(entry["random_ids"])
        return receipt

    def _validate_write(self, operation, target, payload, confirmation, request_id):
        if operation == "message.forward":
            if not isinstance(target, dict) or "source" not in target or "destination" not in target:
                self._reject("invalid_write")
            self._validate_peer(target["source"])
            self._validate_peer(target["destination"])
        else:
            self._validate_peer(target)
        if operation == "message.send":
            text = payload.get("text", "")
            if not text or len(text.encode("utf-16-le")) // 2 > 4096:
                self._reject("invalid_write")
        if operation == "message.forward" and not 1 <= len(payload.get("message_ids", [])) <= 20:
            self._reject("invalid_write")
        expected = {"decision": "allow", "operation": operation, "target": target,
                    "payload": payload, "request_id": request_id}
        for key, value in expected.items():
            if confirmation.get(key) != value:
                self._reject("confirmation_rejected")
        if confirmation.get("expires_at", 0) < self._time:
            self._reject("confirmation_rejected")

    def write(self, session, operation, target, payload, *, request_id, confirmation,
              caller=None, started=None, release=None, fault=None):
        state = self._resolve(session)
        self._check_caller(state, caller)
        if operation not in {"message.send", "message.forward", "message.react", "message.mark_read"}:
            self._reject("unsupported_operation")
        if state["revoked"]:
            self._reject("revoked")
        fingerprint = self._fingerprint(operation, target, payload)
        with self._condition:
            existing = state["journal"].get(request_id)
            if existing is not None:
                if existing["fingerprint"] != fingerprint:
                    self._reject("request_conflict")
                return copy.deepcopy(existing["receipt"])
            self._validate_write(operation, target, payload, confirmation, request_id)
            random_count = 1 if operation == "message.send" else \
                len(payload.get("message_ids", [])) if operation == "message.forward" else 0
            entry = {"fingerprint": fingerprint, "operation": operation, "request_id": request_id,
                     "random_ids": self._random_ids(random_count), "cancelled": False, "release_ready": False}
            entry["receipt"] = self._receipt(entry, "reserved")
            state["journal"][request_id] = entry
            self._audit("write_reserved", operation)
        wire_payload = {"target": copy.deepcopy(target), "payload": copy.deepcopy(payload)}
        self._enter_rpc(state, operation, wire_payload, request_id, None, started)
        entry["receipt"] = self._receipt(entry, "sent")
        try:
            if release is not None:
                def released():
                    release.wait()
                    with self._condition:
                        entry["release_ready"] = True
                        self._condition.notify_all()
                threading.Thread(target=released, daemon=True).start()
                with self._condition:
                    while not entry["release_ready"] and not entry["cancelled"]:
                        self._condition.wait()
            status = "unknown" if fault == "drop_after_commit" or entry["cancelled"] else "confirmed"
            entry["receipt"] = self._receipt(entry, status)
            self._audit("write_outcome", operation)
            return copy.deepcopy(entry["receipt"])
        finally:
            self._exit_rpc(state)

    def cancel(self, session, request_id):
        state = self._resolve(session)
        with self._condition:
            entry = state["journal"].get(request_id)
            if entry is None:
                self._reject("not_found")
            entry["cancelled"] = True
            if entry["receipt"]["status"] == "reserved":
                entry["receipt"] = self._receipt(entry, "canceled")
            elif entry["receipt"]["status"] == "sent":
                entry["receipt"] = self._receipt(entry, "unknown")
            self._condition.notify_all()
            self._audit("write_canceled")
            return copy.deepcopy(entry["receipt"])

    def import_legacy(self, session, blob, *, fault=None):
        state = self._resolve(session)
        if len(json.dumps(blob, separators=(",", ":"))) > 1024 * 1024:
            self._reject("legacy_oversized")
        if blob.get("version") != 1:
            self._reject("legacy_unsupported")
        if not str(blob.get("gotd_session", "")).startswith("synthetic-gotd-session"):
            self._reject("legacy_corrupt")
        if not isinstance(blob.get("api_id"), str) or re.fullmatch(r"[1-9][0-9]*", blob["api_id"]) is None \
                or not isinstance(blob.get("api_hash"), str) or not blob["api_hash"] \
                or not isinstance(blob.get("account_id"), str) \
                or re.fullmatch(r"(0|[1-9][0-9]*)", blob["account_id"]) is None:
            self._reject("legacy_corrupt")
        cursors = blob.get("cursors")
        if not isinstance(cursors, dict) \
                or any(not isinstance(cursors.get(key), str) for key in ("archive", "inbox")):
            self._reject("legacy_corrupt")
        before = copy.deepcopy(state["host"])
        if blob["account_id"] != before["account_id"]:
            self._reject("cross_account")
        if fault == "unavailable":
            self._reject("legacy_unavailable")
        sealed = self._next("sealed")
        self._sealed[sealed] = {"account": blob.get("account_id"),
                                "api_id": blob.get("api_id"), "api_hash": blob.get("api_hash"),
                                "gotd_session": blob.get("gotd_session")}
        staged = {"sealed": sealed, "installation": state["installation"], "connector": state["connector"],
                  "account_id": blob.get("account_id"), "cursors": copy.deepcopy(cursors)}
        if fault == "commit":
            self._reject("import_commit_failed")
        import_id = self._next("import")
        with self._condition:
            state["host"] = staged
            self._imports[import_id] = {"session": session, "before": before}
            self._audit("legacy_import_committed", "session.restore")
        return {"import_id": import_id, "account_id": staged["account_id"],
                "cursors": copy.deepcopy(staged["cursors"])}

    def rollback_import(self, session, import_id):
        state = self._resolve(session)
        record = self._imports.get(import_id)
        if record is None or record["session"] != session:
            self._reject("not_found")
        with self._condition:
            state["host"] = copy.deepcopy(record["before"])
            self._audit("legacy_import_rollback", "session.restore")
        return {"status": "rolled_back"}

    def archive_sync(self, session, chat, pages, *, cursor="", fail_after=None):
        state = self._resolve(session)
        chat_id = chat["id"]
        archive = state["archive"].setdefault(chat_id, {"seen": [], "inbox_cursor": chat["inbox_cursor"]})
        if cursor:
            try:
                saved = json.loads(cursor)
                archive["seen"] = list(saved["seen"])
                archive["inbox_cursor"] = saved["inbox_cursor"]
            except (ValueError, KeyError, TypeError):
                self._reject("legacy_corrupt")
        seen = set(archive["seen"])
        messages = []
        completed = 0
        for page in pages:
            if fail_after is not None and completed >= fail_after:
                break
            for message in page:
                if message["id"] not in seen:
                    seen.add(message["id"])
                    messages.append(copy.deepcopy(message))
            completed += 1
            archive["seen"] = sorted(seen)
            archive["inbox_cursor"] = chat["inbox_cursor"]
        cursor = json.dumps({"seen": archive["seen"], "inbox_cursor": archive["inbox_cursor"]},
                            sort_keys=True, separators=(",", ":"))
        partial = completed < len(pages)
        self._audit("archive_progress", "messages.read")
        return {"status": "partial" if partial else "done", "cursor": cursor,
                "messages": messages, "inbox_cursor": archive["inbox_cursor"],
                "more": partial}

    def download_media(self, session, reference, *, size, moved=False, resume=None,
                       max_parts=None):
        state = self._resolve(session)
        offset = 0
        if resume is not None:
            token = self._resume_tokens.get(resume)
            if token is None or token["reference"] != reference:
                self._reject("stale_session")
            if token["connector"] != state["connector"]:
                self._reject("cross_extension")
            if token["installation"] != state["installation"]:
                self._reject("cross_installation")
            if token["account"] != state["account"]:
                self._reject("cross_account")
            offset = token["offset"]
        if size < offset or size - offset > self.byte_budget - state["media_bytes"]:
            self._reject("byte_budget_exceeded")
        media = state["media"].setdefault(reference, {"file_reference": "old-file-reference"})
        if moved:
            self._file_counter += 1
            media["file_reference"] = f"refreshed-file-reference-{self._file_counter}"
        lengths = []
        while offset + sum(lengths) < size:
            length = min(512 * 1024, size - offset - sum(lengths))
            if max_parts is not None and len(lengths) >= max_parts:
                break
            lengths.append(length)
        parts = [b"\0" * length for length in lengths]
        offset += sum(lengths)
        self.metrics["media_bytes"] += sum(lengths)
        state["media_bytes"] += sum(lengths)
        token = None
        if offset < size:
            token = self._next("resume")
            self._resume_tokens[token] = {"reference": reference, "installation": state["installation"],
                                          "connector": state["connector"], "account": state["account"],
                                          "offset": offset}
        media["offset"] = offset
        self._audit("media_download", "media.download")
        return {"reference": reference, "file_reference": media["file_reference"],
                "parts": parts, "resume": token}

    def public_snapshot(self, session):
        state = self._resolve(session)
        return {"account": state["account"], "authorized": state["authorized"] and not state["revoked"],
                "local_transport": state["local_transport"],
                "remote_session_revoked": state["remote_session_revoked"],
                "pending_updates": len(state["pending"]), "gap": state["gap"]}

    def neighbor_call(self, session, operation="read"):
        self._resolve(session)
        return {"status": "ok", "operation": operation}

    def evidence(self):
        return {"synthetic": True, "real_account_access": False, "runtime_enabled": False,
                "source_migration_enabled": False}
