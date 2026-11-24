package repositorywire

import "testing"

func TestMirrorValidationHelpers(t *testing.T) {
	for _, valid := range []string{"repo_web", "web", "repo-1.2", "A0_b-c.d"} {
		if !ValidateMirrorRepoID(valid) {
			t.Errorf("expected repo id %q valid", valid)
		}
	}
	for _, bad := range []string{"", ".", "..", "../a", "a/b", "a\\b", ".hidden", "repo..x", "CON", "NUL.txt", "repo."} {
		if ValidateMirrorRepoID(bad) {
			t.Errorf("expected repo id %q invalid", bad)
		}
	}
	for _, valid := range []string{"dev1", "mac-studio.local:1", "node_01"} {
		if !ValidateMirrorMachineID(valid) {
			t.Errorf("expected machine id %q valid", valid)
		}
	}
	for _, bad := range []string{"", "a/b", "a b", "machine\n1"} {
		if ValidateMirrorMachineID(bad) {
			t.Errorf("expected machine id %q invalid", bad)
		}
	}
	if got, err := NormalizeMirrorTTLSeconds(0); err != nil || got != DefaultMirrorFetchTTLSeconds {
		t.Fatalf("default TTL: got %d err %v", got, err)
	}
	if got, err := NormalizeMirrorTTLSeconds(60); err != nil || got != 60 {
		t.Fatalf("explicit TTL: got %d err %v", got, err)
	}
	for _, bad := range []int{-1, MaxMirrorFetchTTLSeconds + 1} {
		if _, err := NormalizeMirrorTTLSeconds(bad); err == nil {
			t.Errorf("expected TTL %d invalid", bad)
		}
	}
	if got, err := NormalizeMirrorSyncIntervalMinutes(0); err != nil || got != DefaultMirrorSyncIntervalMinutes {
		t.Fatalf("default interval: got %d err %v", got, err)
	}
	if got, err := NormalizeMirrorSyncIntervalMinutes(45); err != nil || got != 45 {
		t.Fatalf("explicit interval: got %d err %v", got, err)
	}
	for _, bad := range []int{-1, 5, MinMirrorSyncIntervalMinutes - 1, MaxMirrorSyncIntervalMinutes + 1} {
		if _, err := NormalizeMirrorSyncIntervalMinutes(bad); err == nil {
			t.Errorf("expected interval %d invalid", bad)
		}
	}
}
