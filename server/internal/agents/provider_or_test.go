package agents

import (
	"context"
	"reflect"
	"testing"
)

func TestPersonalAgentLegacyProviderCompatibility(t *testing.T) {
	source := &memSource{name: "demo", sessions: []AgentSession{
		{SessionKey: "a", Provider: "provider-a"},
		{SessionKey: "b", Provider: "provider-b"},
		{SessionKey: "case", Provider: "Provider-A"},
		{SessionKey: "empty", Provider: ""},
	}}
	service := NewService(nil, nil, nil, source)
	for _, test := range []struct {
		provider string
		want     []string
	}{
		{"provider-a", []string{"demo:a"}},
		{"Provider-A", []string{"demo:case"}},
		{"missing", nil},
		{"", []string{"demo:a", "demo:b", "demo:case", "demo:empty"}},
	} {
		list, err := service.Sessions(context.Background(), "", test.provider)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, row := range list {
			got = append(got, row.ID)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("legacy provider %q = %v, want %v", test.provider, got, test.want)
		}
	}
}
