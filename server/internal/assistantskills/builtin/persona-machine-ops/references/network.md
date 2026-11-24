# Network and proxy diagnostics (#12351)

Inspect non-loopback network interfaces, effective proxy configuration (with credentials strictly redacted), and reachability/latency to fixed allowlisted developer endpoints.

## Command and MCP tool

- CLI: `ops.sh network`
- MCP: `machine_ops_network` (no parameters)

## Key response fields

- `interfaces[]`: non-loopback interfaces with `name`, `up`, and `addresses[]`.
- `proxy`: `http_proxy`, `https_proxy`, `all_proxy`, `no_proxy`, and `active`. Embedded usernames and passwords are replaced with `[redacted]`.
- `probes[]`: fixed allowlisted targets (`github_https`, `github_ssh_443`, `npm_registry`, `go_proxy`, `pypi`) with `protocol`, `via_proxy`, `reachable`, `status_code`, `latency_ms`, and `error`.

## Boundary

Probe targets are fixed on the node and never caller-supplied (SSRF prevention).
