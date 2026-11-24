package iphone

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testUDID = "00000000-TESTPHONE0001" // made up

func TestStoreSaveLoadPermissions(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "phone")}
	rec := Record{UDID: testUDID, Name: "Test Phone", ProductType: "iPhone99,1", PairedAt: time.Unix(1, 0).UTC()}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(testUDID); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("no pairing record yet: want ErrNotPaired, got %v", err)
	}
	path, _ := s.RecordPath(testUDID)
	if err := os.WriteFile(path, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(testUDID)
	if err != nil || got.Name != "Test Phone" || got.ProductType != "iPhone99,1" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	for _, p := range []string{s.Dir, filepath.Join(s.Dir, testUDID)} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, want 0700", p, st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(filepath.Join(s.Dir, testUDID, metaFile)); st.Mode().Perm() != 0o600 {
		t.Errorf("device.json mode %v, want 0600", st.Mode().Perm())
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].UDID != testUDID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if err := s.Forget(testUDID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("after Forget: %+v", list)
	}
}

func TestStoreRejectsUnsafeIDs(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, id := range []string{"", "..", "../x", "a/b", "abc", "-leading"} {
		if err := s.Save(Record{UDID: id}); err == nil {
			t.Errorf("Save(%q) accepted", id)
		}
		if _, err := s.RecordPath(id); err == nil {
			t.Errorf("RecordPath(%q) accepted", id)
		}
	}
}

func TestStoreListSkipsIncomplete(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_ = os.MkdirAll(filepath.Join(s.Dir, "00001111-2222"), 0o700) // nothing in it
	_ = os.WriteFile(filepath.Join(s.Dir, "stray.txt"), nil, 0o600)
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("List = %+v, %v", list, err)
	}
}
