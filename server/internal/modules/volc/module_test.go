package volc

import (
	"testing"

	"github.com/nwverse/persona/server/internal/modules/volc/volctest"
)

func TestModuleInfo(t *testing.T) {
	if info := (Module{}).Info(); info.ID != "volc" || info.API != 1 || info.Origin != "builtin" {
		t.Fatalf("%+v", info)
	}
	if err := (Module{}).Register(nil); err != nil {
		t.Fatal(err)
	}
}

// The stand-in names the program's variable as the program does.
func TestStandInEnv(t *testing.T) {
	if volctest.EnvService != EnvService {
		t.Fatalf("volctest.EnvService %q, the program reads %q", volctest.EnvService, EnvService)
	}
}
