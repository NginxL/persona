package recordinghost

import "testing"

// Without a module provided there is no layer; with one, New makes it for each host; providing twice panics.
func TestProvide(t *testing.T) {
	saved := provided
	t.Cleanup(func() { provided = saved })
	provided = nil
	if InBuild() {
		t.Fatal("in the build without a module")
	}
	if _, ok := New(nil); ok {
		t.Fatal("a layer without a module")
	}
	made := 0
	Provide(func(Host) Layer { made++; return nil })
	if _, ok := New(nil); !ok || made != 1 || !InBuild() {
		t.Fatalf("made %d", made)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("provided twice without a panic")
		}
	}()
	Provide(func(Host) Layer { return nil })
}
