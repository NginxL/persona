package machineops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/proto"
)

const mcpInstructions = "Persona read-only machine diagnostics server. Exposes structured read-only inspection for storage mounts/hotspots, " +
	"dev toolchain profiles, redacted network/proxy checks, listening ports/processes, and user systemd/launchd services or Docker containers with bounded logs. " +
	"Mutating machine operations and file management are never exposed here."

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeMCP serves the on-demand read-only machine diagnostics MCP protocol over
// newline-delimited JSON-RPC stdio until stdin closes or ctx is cancelled.
func ServeMCP(ctx context.Context, version string, provider Provider, in io.Reader, out io.Writer) error {
	if provider == nil {
		provider = NewLocalProvider()
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)

	var (
		mu       sync.Mutex
		inflight = map[string]context.CancelFunc{}
		wg       sync.WaitGroup
		werr     error
	)

	for sc.Scan() {
		line := sc.Bytes()
		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
			if id, rerr := rejectMCPLine(line); rerr != nil {
				mu.Lock()
				if err := enc.Encode(mcpResponse{JSONRPC: "2.0", ID: id, Error: rerr}); err != nil && werr == nil {
					werr = err
				}
				mu.Unlock()
			}
			continue
		}
		if len(req.ID) == 0 {
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(req.Params, &p)
				mu.Lock()
				if cancel := inflight[mcpIDKey(p.RequestID)]; cancel != nil {
					cancel()
				}
				mu.Unlock()
			}
			continue
		}

		rctx, cancel := context.WithCancel(ctx)
		key := mcpIDKey(req.ID)
		mu.Lock()
		inflight[key] = cancel
		mu.Unlock()

		wg.Add(1)
		go func(req mcpRequest, key string, rctx context.Context, cancel context.CancelFunc) {
			defer wg.Done()
			res, rerr := handleMCP(rctx, version, provider, req)
			mu.Lock()
			defer mu.Unlock()
			cancelled := rctx.Err() != nil
			delete(inflight, key)
			cancel()
			if cancelled {
				return
			}
			if err := enc.Encode(mcpResponse{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr}); err != nil && werr == nil {
				werr = err
			}
		}(req, key, rctx, cancel)
	}

	wg.Wait()
	stop()
	if werr != nil {
		return werr
	}
	return sc.Err()
}

func handleMCP(ctx context.Context, version string, provider Provider, req mcpRequest) (any, *mcpRPCError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "persona-machine-ops", "version": version},
			"instructions":    mcpInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpToolDefinitions()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &mcpRPCError{Code: -32602, Message: err.Error()}
		}
		if rerr := normalizeCallParams(&p.Name, &p.Arguments); rerr != nil {
			return nil, rerr
		}
		return callMCPTool(ctx, provider, p.Name, p.Arguments), nil
	default:
		return nil, &mcpRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func normalizeCallParams(name *string, args *json.RawMessage) *mcpRPCError {
	*name = strings.TrimSpace(*name)
	if *name == "" {
		return &mcpRPCError{Code: -32602, Message: "tools/call: name is required"}
	}
	trimmed := bytes.TrimSpace(*args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*args = json.RawMessage(`{}`)
		return nil
	}
	if trimmed[0] != '{' {
		return &mcpRPCError{Code: -32602, Message: "tools/call: arguments must be a JSON object"}
	}
	return nil
}

func decodeStrictJSON(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func callMCPTool(ctx context.Context, provider Provider, name string, args json.RawMessage) map[string]any {
	var (
		out any
		err error
	)
	switch name {
	case ToolStorage:
		var empty struct{}
		if err = decodeStrictJSON(args, &empty); err == nil {
			tctx, cancel := context.WithTimeout(ctx, storageTimeout)
			defer cancel()
			out, err = provider.Storage(tctx)
		}
	case ToolStorageHotspots:
		var params proto.StorageHotspotsParams
		if err = decodeStrictJSON(args, &params); err == nil {
			if err = validateHotspotsParams(params); err == nil {
				tctx, cancel := context.WithTimeout(ctx, storageHotspotsTimeout)
				defer cancel()
				out, err = provider.StorageHotspots(tctx, params)
			}
		}
	case ToolToolchain:
		var params proto.MachineToolchainParams
		if err = decodeStrictJSON(args, &params); err == nil {
			tctx, cancel := context.WithTimeout(ctx, toolchainTimeout)
			defer cancel()
			out, err = provider.Toolchain(tctx, params)
		}
	case ToolNetwork:
		var empty struct{}
		if err = decodeStrictJSON(args, &empty); err == nil {
			tctx, cancel := context.WithTimeout(ctx, networkTimeout)
			defer cancel()
			out, err = provider.Network(tctx)
		}
	case ToolPorts:
		var params proto.MachinePortsParams
		if err = decodeStrictJSON(args, &params); err == nil {
			if params, err = validatePortsParams(params); err == nil {
				tctx, cancel := context.WithTimeout(ctx, portsTimeout)
				defer cancel()
				out, err = provider.Ports(tctx, params)
			}
		}
	case ToolProcesses:
		var params proto.MachineProcessesParams
		if err = decodeStrictJSON(args, &params); err == nil {
			if params, err = validateProcessesParams(params); err == nil {
				tctx, cancel := context.WithTimeout(ctx, processesTimeout)
				defer cancel()
				out, err = provider.Processes(tctx, params)
			}
		}
	case ToolServices:
		var empty struct{}
		if err = decodeStrictJSON(args, &empty); err == nil {
			tctx, cancel := context.WithTimeout(ctx, servicesTimeout)
			defer cancel()
			out, err = provider.Services(tctx)
		}
	case ToolServiceLogs:
		var params proto.MachineServiceLogsParams
		if err = decodeStrictJSON(args, &params); err == nil {
			if params, err = validateServiceLogsParams(params); err == nil {
				tctx, cancel := context.WithTimeout(ctx, serviceLogsTimeout)
				defer cancel()
				out, err = provider.ServiceLogs(tctx, params)
			}
		}
	default:
		return mcpTextResult(fmt.Sprintf("unknown or unsupported read-only machine ops tool %q", name), true)
	}
	if err != nil {
		return mcpTextResult(err.Error(), true)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return mcpTextResult(fmt.Sprintf("encode %s result: %v", name, err), true)
	}
	return mcpTextResult(string(encoded), false)
}

func mcpTextResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

func mcpReadOnlyAnnotations(openWorld bool) map[string]any {
	return map[string]any{
		"readOnlyHint":    true,
		"destructiveHint": false,
		"idempotentHint":  true,
		"openWorldHint":   openWorld,
	}
}

func mcpToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        ToolStorage,
			"description": "Read-only per-mount disk and inode utilization on this machine (statfs only, no directory walk).",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolStorageHotspots,
			"description": "On-demand read-only bounded storage hotspot analysis across fixed allowlisted categories (git worktrees, Docker, caches, Persona logs and backups).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"timeout_ms": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Optional scan budget in milliseconds (clamped to node bounds).",
					},
					"max_depth": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"description": "Optional maximum directory walk depth (clamped to node bounds).",
					},
				},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolToolchain,
			"description": "On-demand read-only development toolchain, agent CLI, Docker daemon, GitHub CLI auth status, optional GPU/power hardware, and non-secret environment profile.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"refresh": map[string]any{
						"type":        "boolean",
						"description": "Set true to bypass the 60-second in-process cache.",
					},
				},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolNetwork,
			"description": "On-demand read-only network diagnostics: non-loopback interfaces, credential-redacted proxy config, and fixed allowlisted developer endpoint reachability probes.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(true),
		},
		{
			"name":        ToolPorts,
			"description": "Read-only list of active listening TCP/UDP sockets on this machine with bind scopes and redacted command lines.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"protocol": map[string]any{
						"type":        "string",
						"enum":        []string{"tcp", "udp"},
						"description": "Optional protocol filter: tcp or udp.",
					},
				},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolProcesses,
			"description": "Read-only process list with redacted command lines, Persona protection annotations, and stale/orphan test process heuristics.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sort": map[string]any{
						"type":        "string",
						"enum":        []string{"cpu", "memory", "uptime"},
						"description": "Sort key: cpu (default), memory, or uptime.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Case-insensitive filter matching process name, command line, user, or PID.",
					},
					"orphan_only": map[string]any{
						"type":        "boolean",
						"description": "When true, return only likely stale/orphan test processes.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     300,
						"description": "Maximum number of processes to return (default 100, capped at 300).",
					},
				},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolServices,
			"description": "Read-only inspection of user systemd/launchd services, Docker containers, and service manager availability on this machine.",
			"inputSchema": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
		{
			"name":        ToolServiceLogs,
			"description": "Read-only bounded tail logs for a validated user systemd unit, launchd label, or Docker container (clamped to 1-500 lines and 64 KiB).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{
						"type":        "string",
						"enum":        []string{proto.ServiceKindSystemd, proto.ServiceKindLaunchd, proto.ServiceKindDocker},
						"description": "Service manager kind: systemd, launchd, or docker.",
					},
					"id": map[string]any{
						"type":        "string",
						"description": "Validated systemd unit name, launchd label, or Docker container ID/name.",
					},
					"lines": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     proto.ServiceLogsMaxLines,
						"description": "Number of tail lines to fetch (default 100, clamped to [1, 500]).",
					},
				},
				"required":             []string{"kind", "id"},
				"additionalProperties": false,
			},
			"annotations": mcpReadOnlyAnnotations(false),
		},
	}
}

func rejectMCPLine(line []byte) (json.RawMessage, *mcpRPCError) {
	null := json.RawMessage("null")
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, nil
	}
	if !json.Valid(line) {
		return null, &mcpRPCError{Code: -32700, Message: "Parse error"}
	}
	invalid := &mcpRPCError{Code: -32600, Message: "Invalid Request"}
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil || m == nil {
		return null, invalid
	}
	id, ok := m["id"]
	if !ok {
		return nil, nil
	}
	var v any
	_ = json.Unmarshal(id, &v)
	switch v.(type) {
	case string, float64:
		return id, invalid
	}
	return null, invalid
}

func mcpIDKey(id json.RawMessage) string {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
