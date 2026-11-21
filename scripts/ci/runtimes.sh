#!/bin/bash
set -euo pipefail
source "$(dirname "$0")/common.sh"
ci_run 'TypeScript SDK install' 600 bash -c 'cd sdk/ts && npm ci --no-audit --no-fund'
ci_run 'TypeScript SDK source authoring, portability, types and tests' 600 bash -c 'cd sdk/ts && npm run test:portable && npm run typecheck && bun test'
for package in integrations/*/package.json; do
  directory="${package%/package.json}"
  ci_run "$directory tests" 300 bash -c 'cd "$1" && npm test' bash "$directory"
done
ci_run 'Pinned OpenCode MCP isolation contract (fake model)' 300 node scripts/ci/opencode-contract.mjs
