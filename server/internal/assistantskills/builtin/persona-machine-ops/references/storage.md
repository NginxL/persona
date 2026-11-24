# Storage and hotspots (#12349)

Inspect per-mount capacity/inodes and bounded hotspot categories without running unbounded `du` scans.

## Commands and MCP tools

- **Per-mount disk and inode stats (fast `statfs` only):**
  - CLI: `ops.sh storage`
  - MCP: `machine_ops_storage` (no parameters)
  - Returns `mounts[]` with `mount`, `device`, `fs_type`, `used_bytes`, `avail_bytes`, `total_bytes`, `used_percent`, `inodes_total`, `inodes_used`, `inodes_free`, `inodes_percent`, and `read_only`.

- **Bounded hotspot analysis (on demand):**
  - CLI: `ops.sh hotspots [--timeout-ms N] [--max-depth N]`
  - MCP: `machine_ops_storage_hotspots` (`{timeout_ms?: int, max_depth?: int}`)
  - Scans only fixed, node-resolved allowlisted categories: `git-worktrees`, `docker`, `user-cache`, `go-build`, `go-mod`, `npm-cache`, `pip-cache`, `persona-logs`, `persona-backups`.
  - Check `partial` and each category's `status` (`ok`, `partial`, `empty`, `unavailable`) and `in_use` flag before recommending any cleanup.

## Boundary

Read-only inspection only. Cleanup execution is never exposed through `ops` CLI or `ops mcp`; recommend specific allowlisted targets for human confirmation in the Web UI or through the session's own approval flow.
