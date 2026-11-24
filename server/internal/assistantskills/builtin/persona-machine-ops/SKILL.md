---
name: persona-machine-ops
description: Opt-in read-only machine diagnostics for storage mounts and hotspots, dev toolchains, redacted network and proxy checks, listening ports and orphan processes, and systemd/launchd services or Docker logs.
---

# Machine operations diagnostics (`persona-machine-ops`)

Use this skill when explicitly asked to inspect or troubleshoot a machine's local health. Prefer the structured read-only CLI or on-demand MCP server over ad-hoc shell commands: every collector applies OS-specific parsing, bounded timeouts, secret redaction, and fixed safe allowlists.

## How to run

- **CLI (local machine):** run `sh <this-skill-dir>/scripts/ops.sh <command> [flags]` (or `personad ops <command> [flags]` / `persona-node ops <command> [flags]`). Output is structured JSON on stdout.
- **On-demand read-only MCP server:** start `personad ops mcp` (or `sh <this-skill-dir>/scripts/ops.sh mcp`) as a stdio MCP server in an opted-in session to expose the 8 read-only `machine_ops_*` tools.

## Diagnostic domains

Read the matching reference before interpreting fields or flags:

- **Storage and hotspots (#12349).** Per-mount disk/inode utilization (`storage`) and bounded allowlisted hotspot scans (`hotspots`). See [Storage and hotspots](references/storage.md).
- **Toolchain and environment (#12350).** Core dev tools, agent CLIs, Docker daemon socket, `gh` auth status, GPU/power hardware, and non-secret environment summary (`toolchain`). See [Toolchain and environment](references/toolchain.md).
- **Network and proxy (#12351).** Non-loopback interfaces, credential-redacted proxy config, and fixed allowlisted endpoint reachability probes (`network`). See [Network and proxy](references/network.md).
- **Ports and processes (#12352).** Active listening TCP/UDP sockets (`ports`) and running processes with orphan-test heuristics and redacted command lines (`processes`). See [Ports and processes](references/ports-and-processes.md).
- **Services and containers (#12353).** User systemd/launchd services and Docker containers (`services`) with bounded log tailing (`service-logs`). See [Services and containers](references/services.md).

## Safety boundaries

- **Read-only only:** the ops CLI and ops MCP server expose diagnostics only. Never expect or attempt storage cleanup, process termination, or service start/stop/restart through this interface; any remediation must go through the session's own explicit permission and approval flow.
- **No file management:** file browsing and file editing/upload (#12354 / #12403) are human-only in the Web UI and are never exposed through the ops MCP server.
- **Redacted outputs:** proxy credentials, tokens in command lines, and `gh` tokens are redacted by the collectors. Do not bypass redaction by reading secret files or raw environment variables.
