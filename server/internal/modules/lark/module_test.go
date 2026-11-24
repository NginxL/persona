package lark

import "testing"

func TestModuleInfo(t *testing.T) {
	if info := (Module{}).Info(); info.ID != "lark" || info.API != 1 || info.Origin != "builtin" || info.Feature != "module.lark" {
		t.Fatalf("%+v", info)
	}
	if err := (Module{}).Register(nil); err != nil {
		t.Fatal(err)
	}
}
