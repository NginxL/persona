package connector

import "testing"

// ReadOnly is based on the connector's declared ReadTools interface and catalog, never on a tool-name suffix.
func TestRegistryReadOnlyUsesDeclaredTools(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.Add(&fake{id: "example"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"example_read", true},
		{"x_read", false}, // declared by the fake, but not owned under its connector prefix
		{"example_comment", false},
		{"example_send", false},
		{"future_unknown_tool", false},
	} {
		if got := r.ReadOnly(tc.name); got != tc.want {
			t.Errorf("ReadOnly(%q)=%v, want %v", tc.name, got, tc.want)
		}
	}
}
