package voice

import "testing"

func TestModuleInfo(t *testing.T) {
	if info := (Module{}).Info(); info.ID != "voice" || info.API != 1 || info.Origin != "builtin" {
		t.Fatalf("%+v", info)
	}
	if err := (Module{}).Register(nil); err != nil {
		t.Fatal(err)
	}
}
