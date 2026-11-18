#!/bin/bash
# End-to-end check of the push relay on a simulator (docs/operations/push-relay.md "Verification"), without Apple:
#   app's public key → hub code seals a push → local `personad relay` → fake APNs (writes what it got) → `simctl push`
#   → the phone's code opens it. A tampered payload must stay "New message" (the placeholder). What each hop got goes to $OUT
#   (default /tmp/relay-e2e).
#
#   SIM=<udid> scripts/test/relay_sim_e2e.sh
#
# Needs `xcodebuild build-for-testing` done for $SIM (DERIVED: its derived data, default build/DerivedData) and the
# key exported once: PersonaUITests/PushRelayTests/testAllowNotificationsAndExportKey (it also taps "Allow").
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${SIM:?set SIM=<simulator udid>}"
SIM=$(scripts/app/sim-boot.sh "$SIM")   # boots it slim if it is shut down (default profile keeps apsd)
OUT=${OUT:-/tmp/relay-e2e}
BUNDLE=$(python3 scripts/persona_config.py get app.bundle_id)
TMP=$(mktemp -d)
rm -rf "$OUT" && mkdir -p "$OUT"
pids=()
cleanup() { for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null || true; done; rm -rf "$TMP"; }
trap cleanup EXIT

say() { echo "relay-e2e: $*"; }

# 1. The phone's public key, exported by the app (-showPushKey).
container=$(xcrun simctl get_app_container "$SIM" "$BUNDLE" data)
keyfile="$container/Library/Caches/push-key.txt"
[ -s "$keyfile" ] || { say "FAIL: no $keyfile (run PushRelayTests first)"; exit 1; }
PUSH_KEY=$(cat "$keyfile")

# 2. A throwaway APNs key and a fake APNs that keeps each request.
openssl ecparam -name prime256v1 -genkey -noout 2>/dev/null | openssl pkcs8 -topk8 -nocrypt -out "$TMP/key.p8"
APNS_PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
RELAY_PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
cat > "$TMP/apns.py" <<'PY'
import http.server, json, os, sys, itertools
out, n = sys.argv[2], itertools.count(1)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        i = next(n)
        open(os.path.join(out, "apns-%d.json" % i), "wb").write(body)
        json.dump({k.lower(): v for k, v in self.headers.items() if k.lower().startswith("apns-")} | {"path": self.path},
                  open(os.path.join(out, "apns-%d.headers.json" % i), "w"))
        self.send_response(200); self.send_header("Content-Length", "0"); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
python3 "$TMP/apns.py" "$APNS_PORT" "$OUT" & pids+=($!)

# 3. The relay.
cat > "$TMP/relay.json" <<JSON
{"listen": "127.0.0.1:$RELAY_PORT", "apns": {"key_id": "TESTKEY", "team_id": "TESTTEAM", "key_path": "$TMP/key.p8"},
 "topics": ["$BUNDLE"], "secret_path": "$TMP/relay.secret", "apns_base": "http://127.0.0.1:$APNS_PORT"}
JSON
# The caller's GOFLAGS with -trimpath, so checkouts share compiled packages (#10614). -mod=readonly goes on each go
# command line, which overrides GOFLAGS: a -mod=mod there must not rewrite go.mod here.
GOFLAGS=$(sh scripts/test/go_trimpath.sh)
export GOFLAGS
# Built as the release is (server/build-tags.txt).
(cd server && go build -mod=readonly -tags "$(cat build-tags.txt)" -o "$TMP/personad" ./cmd/personad)
"$TMP/personad" relay "$TMP/relay.json" > "$OUT/relay.log" 2>&1 & pids+=($!)
for _ in $(seq 50); do curl -fs "http://127.0.0.1:$RELAY_PORT/relay/v1/health" >/dev/null && break; sleep 0.1; done
curl -fs "http://127.0.0.1:$RELAY_PORT/relay/v1/health" >/dev/null || { say "FAIL: relay did not start"; cat "$OUT/relay.log"; exit 1; }

# 4. Register like the app does (a made-up device token: simctl push does not need a real one).
GRANT=$(curl -fs "http://127.0.0.1:$RELAY_PORT/relay/v1/register" -H 'Content-Type: application/json' \
  -d "{\"apns_token\":\"$(openssl rand -hex 32)\",\"env\":\"sandbox\",\"topic\":\"$BUNDLE\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["grant"])')

# 5. The hub's code seals and pushes (an interaction, then a finished turn).
for kind in interaction done; do
  (cd server && PERSONA_RELAY_URL="http://127.0.0.1:$RELAY_PORT/relay" PERSONA_RELAY_GRANT="$GRANT" PERSONA_PUSH_KEY="$PUSH_KEY" \
    PERSONA_RELAY_KIND=$kind go test -mod=readonly ./internal/hub/ -run TestManualRelayPush -count=1 >"$OUT/hub-$kind.log" 2>&1) \
    || { say "FAIL: hub push ($kind)"; cat "$OUT/hub-$kind.log"; exit 1; }
done
[ -s "$OUT/apns-2.json" ] || { say "FAIL: fake APNs got nothing"; exit 1; }

# What the relay sent must not contain the text the hub pushed.
if grep -lE '重构支付|swift test|笔记本|测试全过' "$OUT"/apns-*.json; then say "FAIL: plaintext reached APNs"; exit 1; fi
say "APNs got only sealed payloads: $(cat "$OUT/apns-1.json" | head -c 160)…"

# 6. Open them as the phone does. `simctl push` does not run notification service extensions (the simulator adds the
#    request like a local one), so the extension's work (PushCrypto.service) runs in a test hosted in the app, with
#    the same keychain group and key. A tampered copy must stay "New message".
python3 - "$OUT/apns-1.json" "$OUT/garbage.json" <<'PY'
import json, sys, base64
p = json.load(open(sys.argv[1])); raw = bytearray(base64.b64decode(p["e"])); raw[-1] ^= 1
p["e"] = base64.b64encode(bytes(raw)).decode(); json.dump(p, open(sys.argv[2], "w"))
PY
eval "$(python3 scripts/persona_config.py env)"
(cd apple && TEST_RUNNER_RELAY_PAYLOADS="$OUT/apns-1.json=Bash: swift test|$OUT/apns-2.json=改好了，测试全过|$OUT/garbage.json=有新消息" \
  xcodebuild test-without-building -project Persona.xcodeproj -scheme Persona -destination "id=$SIM" \
  -derivedDataPath "${DERIVED:-../build/DerivedData}" -only-testing:PersonaTests/NotificationServiceTests >"$OUT/open.log" 2>&1) \
  || { say "FAIL: the phone side did not open them (see $OUT/open.log)"; grep -E '✘' "$OUT/open.log" | head; exit 1; }
say "OK: hub sealed → relay → APNs request → phone opened all three as expected ($OUT)"
