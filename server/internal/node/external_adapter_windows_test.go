//go:build windows

package node

import "testing"

func TestExternalAdapterWindowsRefusesConfiguration(t *testing.T) {
	t.Setenv(externalAdapterConfigEnv, `C:\example\registry.json`)
	t.Setenv("PATH", "")
	r, err := loadExternalAdapterRegistry()
	if r != nil {
		r.Close()
		t.Fatal("Windows enabled a local adapter")
	}
	externalAdapterTestCode(t, err, "adapter_platform_unsupported")
}
