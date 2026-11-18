package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExternalHistoricalHealthCannotRepairOrRelocate(t *testing.T) {
	s, _, launcher := externalPickFixture(t)
	ctx := context.Background()
	if s.launcherOn(ctx, "server", launcher.Name) {
		t.Fatal("external target became an automatic relocation candidate")
	}
	d, ok := s.device(ctx, "server")
	if !ok {
		t.Fatal("missing fixture computer")
	}
	s.health.mu.Lock()
	s.healthLoadLocked(ctx)
	s.health.recs[healthKey(d.ID, launcher.Name)] = &LauncherHealth{
		Machine: d.ID, Launcher: launcher.Name, Agent: "claude", State: healthFailing,
		Kind: errKindAuth, Fails: 3, CheckedAt: time.Now(),
	}
	s.health.mu.Unlock()
	s.repairMachine(ctx, d, healthConfig{})
	if record, ok := s.launcherHealth(ctx, d.ID, launcher.Name); !ok || record.State != healthFailing || record.Repairs != 0 {
		t.Fatalf("historical external record entered automatic repair: %+v", record)
	}

	s.health.mu.Lock()
	s.health.recs[healthKey(d.ID, launcher.Name)].State = healthOK
	s.health.recs[healthKey(d.ID, "broken")] = &LauncherHealth{
		Machine: d.ID, Launcher: "broken", Agent: "claude", State: healthFailing,
		Kind: errKindAuth, Fails: 3, CheckedAt: time.Now(),
	}
	s.health.mu.Unlock()
	cfg := healthConfig{}
	cfg.repair.Machine, cfg.repair.Launcher = d.ID, "broken"
	err := s.startRepairAgent(ctx, d, []LauncherHealth{{Machine: d.ID, Launcher: "broken"}}, cfg)
	if err == nil || !strings.Contains(err.Error(), "no launcher works") {
		t.Fatalf("historical external success was eligible to run a repair: %v", err)
	}
}
