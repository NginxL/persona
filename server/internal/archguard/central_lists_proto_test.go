package archguard

import (
	"strings"
	"testing"
)

// The freeze of the proto.Features literal: the proto_features row of centralLists (#12901). A domain appends its
// feature flags from an init() in its own file, proto/features_<domain>.go, and internal/proto/features_test.go holds
// the package to that one form of append. So the literal in proto.go takes no new entry.

// The row is appended from this file, as a domain's flags are, so that a PR adding another list's row to the literal
// in central_lists_test.go never meets this one in a hunk, and the test below finds it by id, not by index.
func init() {
	centralLists = append(centralLists, centralList{
		id:     "proto_features",
		source: "internal/proto/proto.go",
		what:   "entry of the Features literal",
		names:  literalEntries("Features"),
		seam: "append a new feature flag from an init() in its domain's own file, proto/features_<domain>.go, beside " +
			"the flag's constant: func init() { Features = append(Features, FeatureX) }",
	})
}

// The proto_features freeze on a planted proto.go: a new entry of the literal, a string or a constant, fails and names
// the seam and the doc; a flag appended from an init() is no entry, so the unplanted source passes with one; a pending
// entry passes once its PR merges.
func TestProtoFeaturesFreeze(t *testing.T) {
	l := centralListByID(t, "proto_features")
	const src = `package proto

const FeatureB = "b"

var Features = []string{
	nodeproto.A, // a comment
	"a", FeatureB,
}

func init() { Features = append(Features, FeatureAppended) }
`
	const list = "\"a\"\nFeatureB\nnodeproto.A\n"
	check := func(src, list string) ([]string, []string) {
		t.Helper()
		failures, notices, err := checkCentralList(l, []byte(src), list)
		if err != nil {
			t.Fatal(err)
		}
		return failures, notices
	}
	if failures, notices := check(src, list); len(failures)+len(notices) != 0 {
		t.Fatalf("unplanted source reports %q, %q", failures, notices)
	}
	for _, c := range []struct{ at, planted, name string }{
		{"var Features = []string{\n", "\t\"planted\",\n", `"planted"`},
		{"\"a\", FeatureB,\n", "\tFeaturePlanted,\n", "FeaturePlanted"},
	} {
		planted := strings.Replace(src, c.at, c.at+c.planted, 1)
		failures, _ := check(planted, list)
		if len(failures) != 1 || !strings.Contains(failures[0], " "+c.name+" is a new ") ||
			!strings.Contains(failures[0], "proto/features_<domain>.go") || !strings.Contains(failures[0], centralListsDoc) {
			t.Errorf("planting %s reports %q, want one failure naming it, proto/features_<domain>.go and the doc",
				c.name, failures)
		}
		if failures, _ := check(planted, list+c.name+" #4512\n"); len(failures) != 0 {
			t.Errorf("%s, pending with its PR, still fails once its PR merges: %q", c.name, failures)
		}
	}
}
