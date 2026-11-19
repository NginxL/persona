package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// machineFact makes unknown distinct from a measured zero, with a reason the model can relay.
type machineFact struct {
	Value  any    `json:"value"`
	Reason string `json:"reason,omitempty"`
}

func known(v any) machineFact           { return machineFact{Value: v} }
func unknown(reason string) machineFact { return machineFact{Reason: reason} }

type machineFacts struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	State                string       `json:"state"`
	StateReason          machineFact  `json:"state_reason"`
	OS                   machineFact  `json:"os"`
	Cores                machineFact  `json:"cores"`
	CPUBusyPercent       machineFact  `json:"cpu_busy_percent"`
	Load                 machineFact  `json:"load_1m"`
	MemoryAvailableBytes machineFact  `json:"memory_available_bytes"`
	Swap                 machineFact  `json:"swap_bytes"`
	DiskFreeBytes        machineFact  `json:"disk_free_bytes"`
	BatteryPercent       *machineFact `json:"battery_percent,omitempty"`
	BatteryCharging      *machineFact `json:"battery_charging,omitempty"`
	TempC                *machineFact `json:"temp_c,omitempty"`
	SampleAgeSeconds     machineFact  `json:"sample_age_seconds"`
	WorkingAgents        machineFact  `json:"working_agents"`
	FailingLaunchers     machineFact  `json:"failing_launchers"`
	NodeOutdated         machineFact  `json:"node_outdated"`
}

// connectionReason follows the same categories as the machine page; unknown errors are not guessed.
func connectionReason(err string) string {
	t := strings.ToLower(err)
	for _, group := range []struct {
		name  string
		terms []string
	}{
		{"authentication", []string{"permission denied", "publickey", "authentication", "unauthorized", "unauthorised", "invalid token", "invalid_token", "forbidden", "host key verification", "access denied", "401", "403"}},
		{"version", []string{"version mismatch", "protocol version", "unknown method", "incompatible"}},
		{"timeout", []string{"timed out", "timeout", "deadline exceeded", "no pong"}},
		{"unreachable", []string{"refused", "no route", "unreachable", "could not resolve", "no such host", "network is", "connection reset", "connection closed", "connection lost", "broken pipe", "eof", "node exited", "waiting for", "lookup ", "dial "}},
	} {
		for _, term := range group.terms {
			if strings.Contains(t, term) {
				return group.name
			}
		}
	}
	return "unknown"
}

func (s *Service) toolMachinesList(ctx context.Context) (string, error) {
	var listed struct {
		Devices  []Device `json:"devices"`
		Complete bool     `json:"complete"`
		Others   []string `json:"others"`
	}
	if err := s.call(ctx, DeviceAll, DeviceDevicesList, map[string]any{}, &listed); err != nil {
		return "", fmt.Errorf("machine list unavailable: %w", err)
	}
	if s.devs == nil {
		return "", fmt.Errorf("machine resource manager unavailable")
	}
	now := time.Now()
	rows := make([]machineFacts, 0, len(listed.Devices))
	for _, d := range listed.Devices {
		m, sample, outdated := s.devs.MachineResources(d.ID)
		row := machineFacts{ID: d.ID, Name: d.Name, State: "pending", StateReason: unknown("connection_pending"),
			OS: unknown("system_not_reported"), Cores: unknown("system_not_reported"),
			CPUBusyPercent: unknown("sample_not_reported"), Load: unknown("sample_not_reported"),
			MemoryAvailableBytes: unknown("sample_not_reported"), Swap: unknown("sample_not_reported"), DiskFreeBytes: unknown("sample_not_reported"),
			SampleAgeSeconds: unknown("sample_not_reported"), WorkingAgents: unknown("connection_not_available"),
			FailingLaunchers: unknown("health_not_available"), NodeOutdated: unknown("connection_not_available")}
		if d.Reachable {
			row.State, row.StateReason = "online", unknown("not_applicable")
		} else if m == nil {
			row.StateReason = unknown("manager_snapshot_unavailable")
		} else if m.Error != "" {
			row.State, row.StateReason = "offline", known(map[string]string{"category": connectionReason(m.Error), "detail": m.Error})
		} else if !d.Online {
			row.State, row.StateReason = "offline", unknown("no_connection_error_reported")
		}
		if m != nil {
			if m.System != nil {
				os := m.System.Name
				if os == "" {
					os = m.System.OS
				}
				if os == "" {
					os = m.OS
				}
				if os != "" {
					row.OS = known(os)
				}
				if m.System.Cores > 0 {
					row.Cores = known(m.System.Cores)
				}
			} else if m.OS != "" {
				row.OS = known(m.OS)
			}
			if d.Reachable && outdated != nil {
				row.NodeOutdated = known(*outdated)
			} else if d.Reachable {
				row.NodeOutdated = unknown("target_version_not_confirmed")
			}
		}
		if sample != nil {
			age := now.Sub(time.Unix(sample.T, 0)).Seconds()
			if age < 0 {
				row.SampleAgeSeconds = unknown("sample_clock_in_future")
				row.CPUBusyPercent, row.Load, row.MemoryAvailableBytes, row.Swap, row.DiskFreeBytes = unknown("sample_clock_in_future"), unknown("sample_clock_in_future"), unknown("sample_clock_in_future"), unknown("sample_clock_in_future"), unknown("sample_clock_in_future")
			} else {
				row.SampleAgeSeconds = known(age)
				reason := "sample_stale"
				if age <= 30 && d.Reachable {
					reason = "metric_not_reported"
					if connected := s.devs.ConnectedAt(d.ID); !connected.IsZero() && sample.T <= connected.Unix() {
						reason = "sample_before_current_connection"
					}
				} else if age <= 30 {
					reason = "connection_not_available"
				}
				row.CPUBusyPercent, row.Load, row.MemoryAvailableBytes, row.Swap, row.DiskFreeBytes = unknown(reason), unknown(reason), unknown(reason), unknown(reason), unknown(reason)
				if reason == "metric_not_reported" {
					if sample.CPU != nil {
						row.CPUBusyPercent = known(*sample.CPU)
					}
					if sample.Load != nil {
						row.Load = known(*sample.Load)
					}
					if sample.MemTotal > 0 && sample.MemUsed > 0 && sample.MemUsed <= sample.MemTotal {
						row.MemoryAvailableBytes = known(sample.MemTotal - sample.MemUsed)
					} else {
						row.MemoryAvailableBytes = unknown("memory_measurement_missing_or_ambiguous")
					}
					if sample.SwapTotal > 0 && sample.SwapUsed >= 0 && sample.SwapUsed <= sample.SwapTotal {
						row.Swap = known(map[string]int64{"used": sample.SwapUsed, "total": sample.SwapTotal})
					} else {
						row.Swap = unknown("swap_measurement_missing_or_ambiguous")
					}
					if sample.DiskTotal > 0 && sample.DiskUsed > 0 && sample.DiskUsed <= sample.DiskTotal {
						row.DiskFreeBytes = known(sample.DiskTotal - sample.DiskUsed)
					} else {
						row.DiskFreeBytes = unknown("disk_measurement_missing_or_ambiguous")
					}
					if sample.BatteryPercent != nil {
						f := known(*sample.BatteryPercent)
						row.BatteryPercent = &f
						if sample.BatteryCharging != nil {
							c := known(*sample.BatteryCharging)
							row.BatteryCharging = &c
						}
					}
					if sample.TempC != nil {
						f := known(*sample.TempC)
						row.TempC = &f
					}
				}
			}
		}
		if d.Reachable {
			list, err := s.sessions(ctx, d.ID)
			if err != nil {
				row.WorkingAgents = unknown("sessions_unavailable")
			} else {
				count := 0
				for _, c := range list {
					if !c.HiddenByDirectory() && !c.Assistant && !c.Archived && c.State == proto.StateWorking {
						count++
					}
				}
				row.WorkingAgents = known(count)
			}
		}
		recs := s.healthRecords(ctx, "", d.ID)
		s.health.mu.Lock()
		loaded := s.health.loaded
		s.health.mu.Unlock()
		if loaded {
			failed := make([]LauncherHealth, 0)
			for _, r := range recs {
				if r.unusable() {
					failed = append(failed, r)
				}
			}
			row.FailingLaunchers = known(failed)
		}
		rows = append(rows, row)
	}
	out, err := json.Marshal(map[string]any{"machines": rows, "complete": listed.Complete, "excluded_count": len(listed.Others)})
	return string(out), err
}
