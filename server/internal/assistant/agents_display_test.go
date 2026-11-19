package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/db"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

type agentsDisplayStore struct {
	*settingsBoardStore
	fail bool
}

func (st *agentsDisplayStore) PutSettingIfUnset(_ context.Context, key string, value any) error {
	st.smu.Lock()
	defer st.smu.Unlock()
	if st.fail {
		return errors.New("offline")
	}
	if _, ok := st.settings[key]; !ok {
		st.settings[key], _ = json.Marshal(value)
	}
	return nil
}
func TestAgentsDisplayDefaultsRolesAndMigration(t *testing.T) {
	st := &agentsDisplayStore{settingsBoardStore: newSettingsBoardStore()}
	s, _ := navigationService(t, st)
	call := func(method, body, role string) (int, agentsDisplay) {
		t.Helper()
		w := boardCall(s, method, "/api/v1/agents/display-settings", body, role)
		var got agentsDisplay
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}
	if code, got := call("GET", "", roleAnswer); code != 200 || !got.ShowBotmux || got.Configured {
		t.Fatalf("default: %d %+v", code, got)
	}
	if code, _ := call("PUT", `{"show_botmux":false}`, roleAnswer); code != http.StatusForbidden {
		t.Fatal(code)
	}
	for _, body := range []string{`{}`, `{"show_botmux":null}`, `{"show_botmux":"false"}`, `nope`} {
		if code, _ := call("PUT", body, roleFull); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	st.fail = true
	if code, _ := call("PUT", `{"show_botmux":false,"only_if_unset":true}`, roleFull); code != 503 {
		t.Fatal(code)
	}
	st.fail = false
	if code, got := call("GET", "", roleFull); code != 200 || got.Configured {
		t.Fatalf("failed save persisted: %d %+v", code, got)
	}
	if code, got := call("PUT", `{"show_botmux":false,"only_if_unset":true}`, roleFull); code != 200 || got.ShowBotmux || !got.Configured {
		t.Fatalf("migration: %d %+v", code, got)
	}
	if code, got := call("PUT", `{"show_botmux":true}`, roleFull); code != 200 || !got.ShowBotmux || !got.Configured {
		t.Fatalf("explicit: %d %+v", code, got)
	}
	if code, got := call("PUT", `{"show_botmux":false,"only_if_unset":true}`, roleFull); code != 200 || !got.ShowBotmux || !got.Configured {
		t.Fatalf("explicit lost: %d %+v", code, got)
	}
	// The value belongs to the store, not a service cache.
	other, _ := navigationService(t, st)
	got, err := other.readAgentsDisplay(context.Background())
	if err != nil || !got.ShowBotmux || !got.Configured {
		t.Fatalf("second service: %+v %v", got, err)
	}
}
func TestAgentsDisplayConcurrentMigration(t *testing.T) {
	st := &agentsDisplayStore{settingsBoardStore: newSettingsBoardStore()}
	s, _ := navigationService(t, st)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			w := boardCall(s, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":false,"only_if_unset":true}`, roleFull)
			if w.Code != 200 {
				t.Errorf("migration: %d", w.Code)
			}
		}()
		go func() {
			defer wg.Done()
			w := boardCall(s, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":true}`, roleFull)
			if w.Code != 200 {
				t.Errorf("explicit: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	got, err := s.readAgentsDisplay(context.Background())
	if err != nil || !got.ShowBotmux || !got.Configured {
		t.Fatalf("explicit choice lost: %+v %v", got, err)
	}
}
func TestAgentsDisplayUnavailable(t *testing.T) {
	s, _ := navigationService(t, nil)
	for _, method := range []string{"GET", "PUT"} {
		w := boardCall(s, method, "/api/v1/agents/display-settings", `{"show_botmux":false}`, roleFull)
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
}

// This exercises the actual INSERT ON CONFLICT boundary across two manager
// connections, rather than relying on a process-local mutex.
func TestAgentsDisplayDatabaseConcurrentMigration(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	one, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	var before json.RawMessage
	had, err := one.TakeSetting(ctx, agentsShowBotmuxKey, &before)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if had {
			_ = one.PutSetting(ctx, agentsShowBotmuxKey, before)
		} else {
			var discard json.RawMessage
			_, _ = one.TakeSetting(ctx, agentsShowBotmuxKey, &discard)
		}
	}()
	a, _ := navigationService(t, one)
	b, _ := navigationService(t, two)
	got, err := a.readAgentsDisplay(ctx)
	if err != nil || got.Configured || !got.ShowBotmux {
		t.Fatalf("unset: %+v %v", got, err)
	}
	w := boardCall(a, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":false,"only_if_unset":true}`, roleFull)
	if w.Code != 200 {
		t.Fatalf("migrate: %d %s", w.Code, w.Body)
	}
	got, err = b.readAgentsDisplay(ctx)
	if err != nil || !got.Configured || got.ShowBotmux {
		t.Fatalf("other connection: %+v %v", got, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			w := boardCall(a, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":false,"only_if_unset":true}`, roleFull)
			if w.Code != 200 {
				t.Errorf("migration: %d %s", w.Code, w.Body)
			}
		}()
		go func() {
			defer wg.Done()
			w := boardCall(b, "PUT", "/api/v1/agents/display-settings", `{"show_botmux":true}`, roleFull)
			if w.Code != 200 {
				t.Errorf("explicit: %d %s", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	got, err = a.readAgentsDisplay(ctx)
	if err != nil || !got.Configured || !got.ShowBotmux {
		t.Fatalf("concurrent explicit lost: %+v %v", got, err)
	}
}
