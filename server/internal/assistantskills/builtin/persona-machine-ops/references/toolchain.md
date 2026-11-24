# Toolchain and environment (#12350)

Inspect installed developer tools, agent CLIs, Docker daemon reachability, GitHub CLI authentication status, optional GPU/power hardware, and non-secret environment facts.

## Command and MCP tool

- CLI: `ops.sh toolchain [--refresh]`
- MCP: `machine_ops_toolchain` (`{refresh?: bool}`)

## Key response fields

- `cached` / `cache_ttl_sec`: results are cached for 60 seconds in long-lived processes; pass `--refresh` / `refresh: true` to force a fresh probe.
- `tools[]` (`git`, `go`, `node`, `npm`, `pnpm`, `python3`, `gh`, `docker`, `pg_dump`, `psql`) and `agent_clis[]` (`claude`, `codex`, `agy`, `omp`): `found`, `version`, `path`, `resolved_path`, `capabilities`, `status`, `note`.
- `docker`: client/server versions, socket path, `socket_accessible`, and `status` (`ready`, `daemon_unreachable`, `permission_denied`, `missing`).
- `github_cli`: `authenticated`, `hosts`, and `status` without exposing tokens.
- `hardware`: optional `chip`, `gpus[]`, and `power` (battery/charging/sleep state).
- `environment`: safe non-secret summary (`shell`, bounded `path_entries`, `version_managers`, `has_custom_go_path`, `proxy_configured`).
