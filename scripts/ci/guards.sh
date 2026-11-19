#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'Repository privacy' 180 python3 scripts/privacy_check.py
if [ -n "${CI_BASE_SHA:-}" ]; then
  ci_run 'Commit and changed-file privacy' 180 python3 scripts/privacy_check.py --range "${CI_BASE_SHA}..HEAD"
fi
# CI_BASE_SHA when the workflow set it, otherwise the clone's default branch. A migration already on that base
# cannot be edited, deleted or renamed (comment- and whitespace-only edits pass), and a new one is named by the UTC
# second it was written (YYYYMMDDHHMMSS_<name>.sql), not a hand-picked number.
ci_run 'Frozen migrations' 60 python3 scripts/ci/migration_freeze.py
# Fixed time.Sleep calls in Go tests may stay or fall, never rise. A // sleep-ok: <reason> line does not count.
ci_run 'Fixed sleeps' 60 python3 scripts/ci/sleep_ratchet.py
# Hard-coded px font sizes in web/src may stay or fall, never rise (docs/design/task-cards.md 4.1).
ci_run 'Web font sizes' 60 python3 scripts/ci/web_font_ratchet.py
ci_run 'Prompt language migration' 60 python3 scripts/ci/prompt_language_ratchet.py
ci_run 'Case-sensitive paths and external references' 180 bash -c 'python3 scripts/test/test_path_case.py && python3 scripts/test/test_external_service_refs.py'
# No Apple source imports CallKit and no build setting links it: App Review rejects that for China (#9290,
# docs/operations/release.md "No CallKit"). scripts/ci/apple.sh checks the built apps' binaries too.
ci_run 'No CallKit in Apple sources and build settings' 120 python3 scripts/test/test_apple_no_callkit.py
# Generated wire types (schema/extension, schema/wire) match their schemas; fixture samples hold to schema/wire; no
# hand-written copy of a generated type returns (#7891 stage 5).
ci_run 'Generated wire types' 120 python3 scripts/test/test_wire_schema.py
