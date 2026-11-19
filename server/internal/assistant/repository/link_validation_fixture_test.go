package repository

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Web draft admission and manager normalization consume the same link cases.
func TestSharedLinkValidationFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../../../spec/fixtures/task-repositories/link-validation.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name          string      `json:"name"`
		Provider      string      `json:"provider"`
		RepositoryURL string      `json:"repository_url"`
		Links         []LinkInput `json:"links"`
		Valid         bool        `json:"valid"`
		Error         string      `json:"error"`
		Expected      []TaskLink  `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			identity, err := Parse(tc.RepositoryURL, tc.Provider)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(tc.Links)
			got, err := NormalizeLinks(identity, tc.Links)
			after, _ := json.Marshal(tc.Links)
			if string(before) != string(after) {
				t.Fatal("normalization changed the input or discarded source metadata")
			}
			if !tc.Valid {
				if err == nil || !strings.Contains(err.Error(), tc.Error) || got != nil {
					t.Fatalf("got %+v, %v; want atomic rejection with %q", got, err, tc.Error)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.Expected) {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.Expected)
			}
		})
	}
}
