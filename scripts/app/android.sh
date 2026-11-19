#!/bin/bash
# Build the Android app and install it on a connected phone or emulator (docs/android/android.md).
#   ./scripts/app/android.sh            # debug build, install, launch
#   ./scripts/app/android.sh test       # unit tests only
#   ./scripts/app/android.sh lint       # Android lint
#   ./scripts/app/android.sh shots -s <emulator> --out <directory>
#   ./scripts/app/android.sh shots-live -s <emulator> --scenario <scene.json> --theme light|dark|both --out <directory>
#   ./scripts/app/android.sh apk        # debug APK only (android/app/build/outputs/apk/debug/app-debug.apk)
#   ./scripts/app/android.sh release    # R8 release APK, checked, aligned and signed for your own phone
#                                       # (android/app/build/outputs/apk/release/app-release.apk); add -s <phone> to install it
# Gradle runs with at most ANDROID_MAX_WORKERS workers (default 4), unless org.gradle.workers.max is already set.
# shots takes optional environment variables for repeatable, pixel-comparable captures (docs/android/android.md):
#   SHOTS_STYLE=glass|brief|board  pass debug.style on every launch
#   SHOTS_PLUGINS=1                the mock hub serves MOCK_EXTENSIONS (Settings › Plugins) and the plugin pages are captured too
#                                  (a plugin's own page, the plugin market and a launcher's page with a plugin's section);
#                                  off by default because its running voice plugin adds the voice button to Persona
#   SHOTS_MOCK_PORT=<port>         run the mock hub on this port instead of a random one
#   SHOTS_FROZEN=<ISO time>        e.g. 2026-11-02T04:00:00+00:00: freeze the mock hub's and the emulator's clock,
#                                  turn animations off, put the status bar in demo mode (needs adb root) and
#                                  capture each page only once it has held still
#   SHOTS_GETTING_STARTED_URL=<https URL>  show the welcome page's "No hub yet?" link in the capture (default: none, so captures
#                                  do not depend on the local config)
#   SHOTS_SETTLE=<seconds>         wait after each launch before the capture (default 2)
# Compare two capture directories with scripts/app/android_shots_diff.py.
# The application id comes from ~/.config/persona/config.json: android.application_id, else app.bundle_id.
# The welcome page's "No hub yet?" link comes from the same file: publish.getting_started_url (https only; none: no link).
# release signs with android.release_keystore (and android.release_key_alias, android.release_keystore_password_command,
# android.release_key_password_command) from the same file; with none configured it uses the SDK's local debug keystore
# (~/.android/debug.keystore), the key debug builds on this computer are signed with, so it updates one in place.
# A debug build is debuggable, so ART never compiles it ahead of time, and R8 does not shrink it: use release on the
# phone you actually use (docs/android/android.md "Debug against release").
set -euo pipefail
cd "$(dirname "$0")/../.."
COMMAND=install
SERIAL=""
OUTPUT=""
SCENARIO=""
THEME=both
while [ "$#" -gt 0 ]; do
  case "$1" in
    -s|--serial) SERIAL="${2:?需要模拟器序列号}"; shift 2 ;;
    --out) OUTPUT="${2:?需要输出目录}"; shift 2 ;;
    --scenario) SCENARIO="${2:?需要场景文件}"; shift 2 ;;
    --theme) THEME="${2:?需要 light、dark 或 both}"; shift 2 ;;
    install|apk|release|test|lint|shots|shots-live) COMMAND="$1"; shift ;;
    *) echo "用法：$0 [install|apk|release|test|lint|shots|shots-live] [-s 模拟器] [--out 目录]" >&2; exit 2 ;;
  esac
done
if [ "$COMMAND" = shots-live ]; then
  SCENARIO=${SCENARIO:-scripts/app/android_adaptive_scenes/window-selftest.json}
  [ -f "$SCENARIO" ] || { echo "场景文件不存在：$SCENARIO" >&2; exit 2; }
  SCENARIO="$(cd "$(dirname "$SCENARIO")" && pwd)/$(basename "$SCENARIO")"
  case "$THEME" in light|dark|both) ;; *) echo "--theme 只能是 light、dark 或 both。" >&2; exit 2 ;; esac
elif [ -n "$SCENARIO" ] || [ "$THEME" != both ]; then
  echo "--scenario 和 --theme 只用于 shots-live。" >&2; exit 2
fi
if [ "$COMMAND" = shots ] || [ "$COMMAND" = shots-live ]; then
  APP_ID=com.example.persona.shots
else
APP_ID=$(python3 scripts/persona_config.py get android.application_id 2>/dev/null || true)
[ -n "$APP_ID" ] || APP_ID=$(python3 scripts/persona_config.py get app.bundle_id 2>/dev/null || true)
APP_ID=${APP_ID:-com.example.persona}
fi
ADB_ARGS=()
[ -z "$SERIAL" ] || ADB_ARGS=(-s "$SERIAL")
SDK=${ANDROID_HOME:-$HOME/Library/Android/sdk}
ADB="$SDK/platform-tools/adb"
# Gradle needs a current launcher JVM even when the daemon JVM criteria pins JDK 21.
JBR="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
JAVA_BIN="${JAVA_HOME:+$JAVA_HOME/bin/}java"
JAVA_VERSION=$("$JAVA_BIN" -version 2>&1 | head -n 1 || true)
JAVA_MAJOR=$(printf '%s' "$JAVA_VERSION" | sed -E 's/.*version "([0-9]+)(\.([0-9]+))?.*/\1/; s/^1$/8/')
if ! [[ "$JAVA_MAJOR" =~ ^[0-9]+$ ]] || [ "$JAVA_MAJOR" -lt 21 ]; then
  if [ -x "$JBR/bin/java" ]; then
    export JAVA_HOME="$JBR"
  else
    echo "构建需要 JDK 21，请设置 JAVA_HOME。" >&2
    exit 1
  fi
fi
if [ "$COMMAND" = shots ] || [ "$COMMAND" = shots-live ]; then
  case "$SERIAL" in emulator-*) ;; *) echo "shots / shots-live 必须用 -s 指定专用模拟器。" >&2; exit 2 ;; esac
  [ "$("$ADB" -s "$SERIAL" shell getprop ro.kernel.qemu | tr -d '\r')" = 1 ] || { echo "指定设备不是已启动的模拟器。" >&2; exit 2; }
  case "${SHOTS_STYLE:-}" in ""|glass|brief|board) ;; *) echo "SHOTS_STYLE 只能是 glass、brief 或 board。" >&2; exit 2 ;; esac
  case "${SHOTS_PLUGINS:-}" in ""|0|1) ;; *) echo "SHOTS_PLUGINS 只能是 1 或 0。" >&2; exit 2 ;; esac
  if [ -n "${SHOTS_MOCK_PORT:-}" ]; then
    if ! [[ "$SHOTS_MOCK_PORT" =~ ^[0-9]+$ ]] || [ "$SHOTS_MOCK_PORT" -lt 1024 ] || [ "$SHOTS_MOCK_PORT" -gt 65535 ]; then
      echo "SHOTS_MOCK_PORT 必须是 1024 到 65535 之间的端口号。" >&2; exit 2
    fi
    if lsof -nP -iTCP:"$SHOTS_MOCK_PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo "端口 $SHOTS_MOCK_PORT 已被占用。" >&2; exit 2; fi
  fi
  SETTLE=${SHOTS_SETTLE:-2}
  [[ "$SETTLE" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "SHOTS_SETTLE 必须是秒数。" >&2; exit 2; }
  # The frozen instant in the form toybox `date -u` sets: MMDDhhmmCCYY.ss (UTC).
  FROZEN_SET=""
  if [ -n "${SHOTS_FROZEN:-}" ]; then
    FROZEN_SET=$(python3 -c '
import sys
from datetime import datetime, timezone
moment = datetime.fromisoformat(sys.argv[1])
if moment.tzinfo is None or moment.microsecond: sys.exit(1)
print(moment.astimezone(timezone.utc).strftime("%m%d%H%M%Y.%S"))' "$SHOTS_FROZEN" 2>/dev/null) || {
      echo "SHOTS_FROZEN 必须是带时区、精确到秒的 ISO 时间，例如 2026-11-02T04:00:00+00:00。" >&2; exit 2; }
  fi
  OUTPUT=${OUTPUT:-/tmp/persona-android-shots-$(date +%Y%m%d-%H%M%S)}
  mkdir -p "$OUTPUT"
  OUTPUT=$(cd "$OUTPUT" && pwd)
fi
cd android
if [ "$COMMAND" = shots ] || [ "$COMMAND" = shots-live ]; then
  GETTING_STARTED=${SHOTS_GETTING_STARTED_URL:-}
else
  GETTING_STARTED=$(python3 ../scripts/persona_config.py get publish.getting_started_url 2>/dev/null || true)
fi
[ -f local.properties ] || echo "sdk.dir=$SDK" > local.properties
# Gradle runs in a machine-wide gradle slot, so parallel worktrees queue instead of thrashing
# (docs/operations/testing.md "Heavy build slots"). Inside the slot it takes at most ANDROID_MAX_WORKERS workers
# (default 4; org.gradle.parallel is on) unless the caller already set org.gradle.workers.max in GRADLE_OPTS or the
# Gradle user home's properties.
gradlew() {
  local workers=()
  if ! { [[ " ${GRADLE_OPTS:-} " == *" -Dorg.gradle.workers.max="* ]] ||
         grep -qs '^[[:space:]]*org\.gradle\.workers\.max[[:space:]]*=' "${GRADLE_USER_HOME:-$HOME/.gradle}/gradle.properties"; }; then
    workers=(--max-workers="${ANDROID_MAX_WORKERS:-4}")
  fi
  python3 ../scripts/heavy_slot.py run --kind gradle -- ./gradlew ${workers[@]+"${workers[@]}"} "$@"
}
case "${ANDROID_MAX_WORKERS:-4}" in ''|*[!0-9]*|0*) echo "ANDROID_MAX_WORKERS must be a positive whole number." >&2; exit 2 ;; esac
case "$COMMAND" in
  test) gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" test ;;
  lint) gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" lint ;;
  apk) gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" :app:assembleDebug && ls -la app/build/outputs/apk/debug/app-debug.apk ;;
  release)
    config() { python3 ../scripts/persona_config.py get "$1" 2>/dev/null || true; }
    # || true: with no build-tools ls fails, and under pipefail the script would stop here without the message below
    TOOLS=$( (ls -d "$SDK"/build-tools/*/ 2>/dev/null || true) | sort -V | tail -n 1)
    [ -x "${TOOLS}apksigner" ] && [ -x "${TOOLS}zipalign" ] || { echo "找不到 build-tools 里的 apksigner / zipalign（$SDK/build-tools）。" >&2; exit 1; }
    KEYSTORE=$(config android.release_keystore)
    if [ -n "$KEYSTORE" ]; then
      KEYSTORE=${KEYSTORE/#\~/$HOME}
      KEY_ALIAS=$(config android.release_key_alias)
      STORE_PASSWORD_COMMAND=$(config android.release_keystore_password_command)
      KEY_PASSWORD_COMMAND=$(config android.release_key_password_command)
      [ -f "$KEYSTORE" ] || { echo "android.release_keystore 指向的文件不存在：$KEYSTORE" >&2; exit 1; }
      [ -n "$KEY_ALIAS" ] && [ -n "$STORE_PASSWORD_COMMAND" ] || { echo "配置了 android.release_keystore 时，还需要 android.release_key_alias 和 android.release_keystore_password_command。" >&2; exit 1; }
      STORE_PASSWORD=$(bash -c "$STORE_PASSWORD_COMMAND")
      KEY_PASSWORD=$STORE_PASSWORD
      [ -z "$KEY_PASSWORD_COMMAND" ] || KEY_PASSWORD=$(bash -c "$KEY_PASSWORD_COMMAND")
    else
      # The SDK's own debug key: every debug build on this computer has it, so the release updates one in place.
      KEYSTORE="$HOME/.android/debug.keystore"
      KEY_ALIAS=androiddebugkey
      STORE_PASSWORD=android
      KEY_PASSWORD=android
      [ -f "$KEYSTORE" ] || { echo "没有配置 android.release_keystore，也没有 $KEYSTORE（先编一次 debug 包会生成它）。" >&2; exit 1; }
      echo "未配置 android.release_keystore：用本机的 debug 密钥签名，只适合装在自己的手机上。" >&2
    fi
    [ -n "$STORE_PASSWORD" ] && [ -n "$KEY_PASSWORD" ] || { echo "密钥库口令命令没有输出。" >&2; exit 1; }
    case "$STORE_PASSWORD$KEY_PASSWORD" in *$'\n'*) echo "密钥库口令命令输出了多行。" >&2; exit 1 ;; esac
    # About shows the commit this APK is built from: this repository's HEAD, passed explicitly (unknown when it cannot be read).
    BUILD_COMMIT=$(git -C .. rev-parse HEAD 2>/dev/null || true)
    [[ $BUILD_COMMIT =~ ^[0-9a-f]{40}$ ]] || BUILD_COMMIT=unknown
    gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" -PpersonaBuildCommit="$BUILD_COMMIT" :app:assembleRelease
    UNSIGNED=app/build/outputs/apk/release/app-release-unsigned.apk
    SIGNED=app/build/outputs/apk/release/app-release.apk
    python3 ../scripts/release/check_android_shrink.py "$UNSIGNED" app/build/outputs/mapping/release --build-tools "$TOOLS"
    rm -f "$SIGNED" "$SIGNED.aligned"
    "${TOOLS}zipalign" -P 16 -f 4 "$UNSIGNED" "$SIGNED.aligned"
    # The passwords reach apksigner alone, in its environment: never a command line, never Gradle and its daemons.
    PERSONA_APK_STORE_PASSWORD="$STORE_PASSWORD" PERSONA_APK_KEY_PASSWORD="$KEY_PASSWORD" "${TOOLS}apksigner" sign \
      --ks "$KEYSTORE" --ks-key-alias "$KEY_ALIAS" --ks-pass env:PERSONA_APK_STORE_PASSWORD \
      --key-pass env:PERSONA_APK_KEY_PASSWORD --out "$SIGNED" "$SIGNED.aligned"
    rm -f "$SIGNED.aligned" "$SIGNED.idsig"
    "${TOOLS}apksigner" verify "$SIGNED"
    ls -la "$SIGNED"
    if [ -n "$SERIAL" ]; then
      "$ADB" -s "$SERIAL" install -r "$SIGNED"
      "$ADB" -s "$SERIAL" shell am start -n "$APP_ID/persona.android.MainActivity" >/dev/null
    fi ;;
  install)
    gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" :app:assembleDebug
    "$ADB" ${ADB_ARGS[@]+"${ADB_ARGS[@]}"} install -r app/build/outputs/apk/debug/app-debug.apk
    "$ADB" ${ADB_ARGS[@]+"${ADB_ARGS[@]}"} shell am start -n "$APP_ID/persona.android.MainActivity" >/dev/null ;;
  shots|shots-live)
    gradlew -q -PpersonaApplicationId="$APP_ID" -PpersonaGettingStartedUrl="$GETTING_STARTED" -Pkotlin.compiler.execution.strategy=in-process :app:assembleDebug
    "$ADB" -s "$SERIAL" install -r app/build/outputs/apk/debug/app-debug.apk >/dev/null
    "$ADB" -s "$SERIAL" shell pm clear "$APP_ID" >/dev/null
    MOCK_PID=""
    MOCK_PORT=""
    SAVED_SETTINGS=""
    DEMO_SINCE=0
    CLOCK_PID=""
    CLOCK_STOP=/data/local/tmp/persona-shots-clock.stop
    STYLE_ARGS=()
    AUTOMATION_ARGS=(--es debug.automation true)
    [ "$COMMAND" != shots-live ] || AUTOMATION_ARGS=(--ez debug.automation true)
    [ -z "${SHOTS_STYLE:-}" ] || STYLE_ARGS=(--es debug.style "$SHOTS_STYLE")
    demo() { "$ADB" -s "$SERIAL" shell am broadcast -a com.android.systemui.demo -e command "$@" >/dev/null; }
    # Set the emulator's clock back to the frozen instant (before each cold start and each capture).
    freeze_clock() {
      [ -z "$FROZEN_SET" ] || "$ADB" -s "$SERIAL" shell date -u "$FROZEN_SET" >/dev/null
    }
    cleanup_shots() {
      local original_status=$?
      local cleanup_failed=0
      cleanup_step() {
        if "$@" >/dev/null 2>&1; then return 0; fi
        if [ "$COMMAND" = shots-live ]; then
          echo "FAIL shots-live cleanup: $*" >&2
          cleanup_failed=1
        fi
        return 0
      }
      cleanup_step "$ADB" -s "$SERIAL" shell am force-stop "$APP_ID"
      if [ "$COMMAND" = shots-live ]; then
        cleanup_step "$ADB" -s "$SERIAL" shell run-as "$APP_ID" rm -f no_backup/hub-data/adaptive-probe.json
        cleanup_step "$ADB" -s "$SERIAL" shell rm -f /data/local/tmp/persona-adaptive-ui.xml
      fi
      if [ -n "$MOCK_PORT" ]; then cleanup_step "$ADB" -s "$SERIAL" reverse --remove "tcp:$MOCK_PORT"; fi
      if [ -n "$MOCK_PID" ]; then
        if kill -0 "$MOCK_PID" 2>/dev/null; then cleanup_step kill "$MOCK_PID"; fi
        wait "$MOCK_PID" 2>/dev/null || true
      fi
      if [ -n "$CLOCK_PID" ]; then
        cleanup_step "$ADB" -s "$SERIAL" shell touch "$CLOCK_STOP"
        if kill -0 "$CLOCK_PID" 2>/dev/null; then cleanup_step kill "$CLOCK_PID"; fi
        wait "$CLOCK_PID" 2>/dev/null || true
      fi
      if [ -n "$SAVED_SETTINGS" ]; then
        # Give the emulator back its clock, animations and status bar.
        cleanup_step demo exit
        for pair in $SAVED_SETTINGS; do
          key=${pair%%=*} value=${pair#*=}
          if [ "$value" = null ]; then cleanup_step "$ADB" -s "$SERIAL" shell settings delete global "$key"
          else cleanup_step "$ADB" -s "$SERIAL" shell settings put global "$key" "$value"; fi
        done
      fi
      if [ "$cleanup_failed" -ne 0 ] && [ "$original_status" -eq 0 ]; then original_status=1; fi
      return "$original_status"
    }
    trap 'cleanup_shots; exit $?' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    if [ -n "$FROZEN_SET" ]; then
      # Setting the clock needs root. Restarting adbd drops reverse forwards, so this runs before adb reverse.
      device_uid() { "$ADB" -s "$SERIAL" shell id -u 2>/dev/null | tr -d '\r'; }
      if [ "$(device_uid)" != 0 ]; then
        "$ADB" -s "$SERIAL" root >/dev/null 2>&1 || true
        for attempt in {1..100}; do
          [ "$(device_uid)" = 0 ] && break
          sleep 0.2
        done
        [ "$(device_uid)" = 0 ] || { echo "模拟器没有以 root 重新连上（adb root 失败或超时）。" >&2; exit 1; }
      fi
      for key in auto_time sysui_demo_allowed animator_duration_scale transition_animation_scale window_animation_scale; do
        SAVED_SETTINGS="$SAVED_SETTINGS $key=$("$ADB" -s "$SERIAL" shell settings get global "$key" | tr -d '\r')"
      done
      "$ADB" -s "$SERIAL" shell settings put global auto_time 0
      # Zero animator scale also puts the app in reduced motion: spinners and breathing light are drawn still.
      for key in animator_duration_scale transition_animation_scale window_animation_scale; do
        "$ADB" -s "$SERIAL" shell settings put global "$key" 0
      done
      freeze_clock
      # Demo mode: leave it first, or icons from the previous round stay; then only a fixed clock and a full battery.
      "$ADB" -s "$SERIAL" shell settings put global sysui_demo_allowed 1
      demo exit
      demo enter
      demo clock -e hhmm "$("$ADB" -s "$SERIAL" shell date +%H%M | tr -d '\r')"
      demo battery -e level 100 -e plugged false -e powersave false
      demo network -e wifi hide -e mobile hide -e airplane hide -e nosim hide -e carriernetworkchange hide
      demo notifications -e visible false
      demo status -e volume hide -e bluetooth hide -e location hide -e alarm hide -e zen hide -e sync hide \
        -e tty hide -e eri hide -e mute hide -e speakerphone hide -e managed_profile hide
      DEMO_SINCE=$SECONDS
      # Pin the clock for the whole run: a loop on the device sets it every 0.1 s, so reads with one-second
      # resolution (the machines charts end at "now") always get the frozen second, however long a page takes
      # to load. It ends on the stop file, or by itself after about an hour; a stale loop is stopped first.
      "$ADB" -s "$SERIAL" shell "touch $CLOCK_STOP; sleep 0.3; rm -f $CLOCK_STOP"
      "$ADB" -s "$SERIAL" shell "n=0; while [ ! -e $CLOCK_STOP ] && [ \$n -lt 36000 ]; do
        date -u $FROZEN_SET >/dev/null; sleep 0.1; n=\$((n + 1)); done" </dev/null >/dev/null 2>&1 &
      CLOCK_PID=$!
    fi
    cd ..
    PLUGINS_MODE=""
    [ "${SHOTS_PLUGINS:-}" != 1 ] || PLUGINS_MODE=1
    if [ "$COMMAND" = shots-live ]; then
      SCENE_MOCK_ENV=$(python3 - "$SCENARIO" <<'SCENE_ENV_PY'
import json, re, sys
with open(sys.argv[1], encoding='utf-8') as source:
    values = json.load(source).get('requires', {}).get('mockEnv', {})
if not isinstance(values, dict): sys.exit('requires.mockEnv must be an object')
for key, value in values.items():
    if not re.fullmatch(r'MOCK_[A-Z0-9_]+', key) or not isinstance(value, str) or any(c in value for c in '\r\n\t'):
        sys.exit('mockEnv keys must be MOCK_* and values must be single-line strings')
    print(key + '\t' + value)
SCENE_ENV_PY
) || { echo "场景 mockEnv 无效。" >&2; exit 2; }
      while IFS=$'\t' read -r key value; do
        [ -n "$key" ] || continue
        export "$key=$value"
      done <<< "$SCENE_MOCK_ENV"
    fi
    PORT=${SHOTS_MOCK_PORT:-0} MOCK_FROZEN=${SHOTS_FROZEN:-} MOCK_EXTENSIONS=$PLUGINS_MODE python3 -u - >"$OUTPUT/mock-hub.log" 2>&1 <<'MOCK_PY' &
import ast, os, re, sys, types
from pathlib import Path
if os.environ.get('MOCK_FROZEN'):
    # Freeze the hub's "now" without touching mock_hub.py: datetime.now() and time.time() return one instant,
    # and uuid4 (the hub id, for one) comes from a seeded generator.
    import datetime as real_datetime, random, threading, time as real_time, uuid
    instant = real_datetime.datetime.fromisoformat(os.environ['MOCK_FROZEN'])
    class FrozenDateTime(real_datetime.datetime):
        @classmethod
        def now(cls, tz=None): return cls.fromtimestamp(instant.timestamp(), tz)
        @classmethod
        def utcnow(cls): return cls.now(real_datetime.timezone.utc).replace(tzinfo=None)
        @classmethod
        def today(cls): return cls.now()
    frozen_datetime = types.ModuleType('datetime')
    frozen_datetime.__dict__.update(vars(real_datetime))
    frozen_datetime.datetime = FrozenDateTime
    sys.modules['datetime'] = frozen_datetime
    frozen_time = types.ModuleType('time')
    frozen_time.__dict__.update(vars(real_time))
    frozen_time.time = instant.timestamp
    frozen_time.time_ns = lambda: round(instant.timestamp()) * 10**9
    sys.modules['time'] = frozen_time
    seeded, seeded_lock = random.Random(0), threading.Lock()
    def seeded_uuid4():
        with seeded_lock: return uuid.UUID(int=seeded.getrandbits(128), version=4)
    uuid.uuid4 = seeded_uuid4
path = Path('scripts/mock/mock_hub.py')
program = ast.parse(path.read_text(), filename=str(path))
main = program.body.pop()
namespace = {'__name__': '__main__', '__file__': str(path)}
exec(compile(program, str(path), 'exec'), namespace)
# Derive anonymous labels from the fixture identities without copying names into the script.
names = {item['sender'] for item in namespace['INBOX'] if item.get('sender')}
replacements = {name: '示例联系人' + str(i + 1) for i, name in enumerate(sorted(names))}
def clean(value):
    if isinstance(value, str):
        for name in sorted(replacements, key=len, reverse=True): value = value.replace(name, replacements[name])
        return re.sub(r'[\w.+-]+@[\w.-]+\.[A-Za-z]+', 'contact@example.com', value)
    if isinstance(value, list):
        value[:] = [clean(v) for v in value]
    elif isinstance(value, dict):
        for k in list(value): value[k] = 'example.com' if k in {'hostname', 'ssh_host', 'imap_host', 'smtp_host'} else clean(value[k])
    return value
for name, value in list(namespace.items()):
    if name.isupper(): namespace[name] = clean(value)
exec(compile(ast.Module(body=[main], type_ignores=[]), str(path), 'exec'), namespace)
MOCK_PY
    MOCK_PID=$!
    for attempt in {1..100}; do
      MOCK_PORT=$(sed -nE 's/^mock hub on .*:([0-9]+)$/\1/p' "$OUTPUT/mock-hub.log" | head -n 1)
      [ -z "$MOCK_PORT" ] || break
      kill -0 "$MOCK_PID" 2>/dev/null || { echo "假 hub 启动失败，请查看 mock-hub.log。" >&2; exit 1; }
      sleep 0.1
    done
    [ -n "$MOCK_PORT" ] || { echo "假 hub 没有就绪。" >&2; exit 1; }
    "$ADB" -s "$SERIAL" reverse "tcp:$MOCK_PORT" "tcp:$MOCK_PORT" >/dev/null
    LOOPBACK=$(python3 -c 'import socket; print(socket.inet_ntoa(bytes((127, 0, 0, 1))))')
    MOCK_ADDRESS="http://$LOOPBACK:$MOCK_PORT"
    # Discover fixture identifiers only from the process started above.
    python3 - "$MOCK_ADDRESS" >"$OUTPUT/fixture.tsv" <<'SHOTS_PY'
import json, sys, urllib.parse, urllib.request
base = sys.argv[1]
def get(path):
    with urllib.request.urlopen(base + path, timeout=5) as response:
        return json.load(response)
sessions = get('/api/v1/sessions')
if isinstance(sessions, dict): sessions = sessions['sessions']
requests = get('/api/v1/interactions')
if isinstance(requests, dict): requests = requests['interactions']
agent = next(s for s in sessions if not s.get('assistant'))
assistant = next((s for s in sessions if s.get('assistant')), None)
print('chat\t%s/%s' % (agent['machine_id'], agent['session_id']))
if assistant: print('assistant-chat\t%s/%s' % (assistant['machine_id'], assistant['session_id']))
if requests:
    r = requests[0]
    print('request\t%s\t%s/%s' % (r['id'], r['machine_id'], r['session_id']))
quote = lambda value: urllib.parse.quote(value, safe='')
print('terminal\tpersona://open?target=terminal&machine=%s&session=%s' % (quote(agent['machine_id']), quote(agent['session_id'])))
inbox = get('/api/v1/inbox')
items = inbox.get('items', []) if isinstance(inbox, dict) else inbox
lark = next((i for i in items if (i.get('lark') or {}).get('feed_id')), None)
mail = next((i for i in items if i.get('source') == 'mail'), None)
if lark: print('lark\tpersona://open?target=lark&chat=%s' % quote(lark['lark']['feed_id']))
if mail: print('mail\tpersona://open?target=inbox&item=%s' % quote(mail['id']))
SHOTS_PY
    "$ADB" -s "$SERIAL" shell pm grant "$APP_ID" android.permission.POST_NOTIFICATIONS >/dev/null 2>&1 || true
    freeze_clock
    # debug.style is saved, but pm clear above wiped it, so every launch passes it again.
    "$ADB" -s "$SERIAL" shell am start -W -n "$APP_ID/persona.android.debug.DebugEntryActivity" \
      --es debug.mockAddress "$MOCK_ADDRESS" --es debug.openTab Agents ${AUTOMATION_ARGS[@]+"${AUTOMATION_ARGS[@]}"} \
      ${STYLE_ARGS[@]+"${STYLE_ARGS[@]}"} >/dev/null
    # Poll the private navigation checkpoint (the pairing-scoped shell position) to wait for pairing and shell creation.
    shell_ready() { "$ADB" -s "$SERIAL" shell run-as "$APP_ID" find no_backup/hub-data -name shell-position.json 2>/dev/null | tr -d '\r' | grep -q 'shell-position.json'; }
    for attempt in {1..100}; do
      if shell_ready; then break; fi
      sleep 0.2
    done
    shell_ready || { echo "假 hub 配对没有完成。" >&2; exit 1; }
    if [ -n "$FROZEN_SET" ]; then
      # With the mobile network hidden no connection is in service, so Android 15 shows a satellite icon about 5 s
      # later (no demo command or icon_blacklist removes it). Wait it out so every capture has the same status bar.
      while [ $((SECONDS - DEMO_SINCE)) -lt 8 ]; do sleep 0.5; done
    fi
    if [ "$COMMAND" = shots-live ]; then
      THEMES=("$THEME")
      [ "$THEME" != both ] || THEMES=(light dark)
      RESET_ARGS=()
      for theme in ${THEMES[@]+"${THEMES[@]}"}; do
        # Each theme gets one fresh initial task so its first debug route is applied by initialLaunch.
        # The Python runner never force-stops between the steps of that theme.
        "$ADB" -s "$SERIAL" shell am force-stop "$APP_ID" >/dev/null
        ADB="$ADB" APP_ID="$APP_ID" SERIAL="$SERIAL" MOCK_ADDRESS="$MOCK_ADDRESS" OUTPUT="$OUTPUT" \
          SHOTS_STYLE="${SHOTS_STYLE:-}" SHOTS_SETTLE="$SETTLE" SHOTS_FROZEN="${SHOTS_FROZEN:-}" \
          python3 scripts/app/android_adaptive_shots.py --scenario "$SCENARIO" --theme "$theme" --out "$OUTPUT" \
            ${RESET_ARGS[@]+"${RESET_ARGS[@]}"}
        RESET_ARGS=(--reset)
      done
      echo "同进程截图已输出到：$OUTPUT"
      exit 0
    fi
    capture() {
      local theme="$1" name="$2"
      shift 2
      "$ADB" -s "$SERIAL" shell am force-stop "$APP_ID"
      freeze_clock
      "$ADB" -s "$SERIAL" shell am start -W -n "$APP_ID/persona.android.debug.DebugEntryActivity" \
        --es debug.theme "$theme" --es debug.automation true ${STYLE_ARGS[@]+"${STYLE_ARGS[@]}"} "$@" >/dev/null
      sleep "$SETTLE"
      freeze_clock
      "$ADB" -s "$SERIAL" exec-out screencap -p >"$OUTPUT/$theme-$name.png"
      if [ -n "$FROZEN_SET" ]; then
        # On a busy machine pages are still loading or scrolling after the settle time: capture again until three
        # frames in a row are the same (the page held still for about a second), for at most 20 s.
        local still=0 deadline=$((SECONDS + 20))
        while [ "$still" -lt 2 ] && [ "$SECONDS" -lt "$deadline" ]; do
          sleep 0.3
          "$ADB" -s "$SERIAL" exec-out screencap -p >"$OUTPUT/.frame.png"
          if cmp -s "$OUTPUT/.frame.png" "$OUTPUT/$theme-$name.png"; then still=$((still + 1))
          else still=0; mv "$OUTPUT/.frame.png" "$OUTPUT/$theme-$name.png"; fi
        done
        rm -f "$OUTPUT/.frame.png"
        [ "$still" -ge 2 ] || echo "页面 20 秒内没有停下来，按最后一帧保存：$theme-$name" >&2
      fi
      [ -s "$OUTPUT/$theme-$name.png" ] || { echo "截图失败：$name" >&2; exit 1; }
    }
    for theme in light dark; do
      for tab in Persona Agents Inbox Settings; do capture "$theme" "$tab" --es debug.openTab "$tab"; done
      while IFS=$'\t' read -r page target request_session <&3; do
        case "$page" in
          chat) capture "$theme" chat --es debug.openSession "$target" --es debug.conversationKind agent ;;
          assistant-chat) capture "$theme" assistant-chat --es debug.openSession "$target" --es debug.conversationKind assistant ;;
          request) capture "$theme" request --es debug.openRequest "$target" --es debug.session "$request_session" --es debug.conversationKind agent ;;
          # adb shell hands the command line to the device shell, so links with & are quoted again for it.
          terminal|lark|mail) capture "$theme" "$page" --es debug.openLink "'$target'" ;;
        esac
      done 3<"$OUTPUT/fixture.tsv"
      capture "$theme" pairing --es debug.openPairing true
      capture "$theme" diagnostics --es debug.openDiagnostics true
      for page in notifications readiness; do capture "$theme" "settings-$page" --es debug.openLink "'persona://open?target=settings&page=$page'"; done
      for page in machines terminals knowledge phone catalog; do capture "$theme" "$page" --es debug.openPage "$page"; done
      # Settings › Managers (docs/assistant/managers.md): the mock's roster, a manager on another computer and the hub's own (Delete off).
      # The install code sheet is drawn by ManagersShotsTest (feature/settings, build/managers-shots/).
      capture "$theme" settings-managers --es debug.openPage managers
      for id in m-3fa21c assistant; do capture "$theme" "manager-$id" --es debug.openPage "manager:$id"; done
      # Settings › Model configuration and Add model provider, typed in and from a plugin's preset (the mock offers two example presets).
      capture "$theme" settings-modelconfig --es debug.openLink "'persona://open?target=settings&page=modelconfig'"
      for mode in manual preset; do
        capture "$theme" "new-provider-$mode" --es debug.openLink "'persona://open?target=settings&page=modelconfig'" --es debug.newProvider "$mode"
      done
      if [ -n "$PLUGINS_MODE" ]; then
        # Settings › Plugins and three of the mock's plugins: one to set up (every kind of field), hub.json's, a broken one.
        capture "$theme" settings-plugins --es debug.openLink "'persona://open?target=settings&page=plugins'"
        for id in example-notes voice example-weather example-gateway; do capture "$theme" "plugin-$id" --es debug.openPage "plugin:$id"; done
        # A page a plugin draws (every kind of item), the plugin market (the catalog, an upgradable plugin, one this hub cannot
        # install, the markets) and a launcher's page with a plugin's section (#1329).
        capture "$theme" plugin-ui --es debug.openPage "plugin-ui:example-gateway:home"
        capture "$theme" plugin-market --es debug.openPage market
        capture "$theme" plugin-market-upgrade --es debug.openPage "market-plugin:3f2a9c1d0b:example-gateway"
        capture "$theme" plugin-market-unavailable --es debug.openPage "market-plugin:8e01b7c4a2:team-windows"
        capture "$theme" plugin-markets --es debug.openPage markets
        capture "$theme" launcher --es debug.openPage "launcher:macbook:claude"
        # UI slots (#1451): the settings home with the plugins' card and nav pages, and a nav page (charts, number grid).
        capture "$theme" settings-plugin-slots --es debug.openTab Settings
        capture "$theme" plugin-nav-page --es debug.openPage "plugin-ui:example-gateway:usage"
      fi
    done
    echo "截图已输出到：$OUTPUT"
    ;;
  *) echo "usage: $0 [install|apk|test|lint]" >&2; exit 2 ;;
esac
