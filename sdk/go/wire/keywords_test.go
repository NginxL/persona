package wire

import (
	"reflect"
	"testing"
)

// check reads a schema node's keywords only through member: for every node of
// the loaded schemas, and every refSiblings rest, member must answer exactly
// what a plain lookup does, so skipping absent keywords changes no result.
func TestKeywordsMatchSchemaNodes(t *testing.T) {
	loadOnce.Do(loadSchemas)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	nodes := 0
	var walk func(any)
	visit := func(s map[string]any) {
		nodes++
		have := keywordsOf(s)
		if have != keywordsIn(s) {
			t.Fatalf("present has %b for a node with %b", have, keywordsIn(s))
		}
		for name, bit := range keywordBits {
			got, want := member(s, have, bit, name), s[name]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("member %s = %v, the node has %v", name, got, want)
			}
		}
	}
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			visit(value)
			if _, ok := value["$ref"].(string); ok {
				visit(refSiblings(value))
			}
			for _, m := range value {
				walk(m)
			}
		case []any:
			for _, m := range value {
				walk(m)
			}
		}
	}
	for _, doc := range files {
		walk(doc)
	}
	if nodes < 1000 || len(present) < nodes/2 {
		t.Fatalf("walked %d nodes, present holds %d", nodes, len(present))
	}
}

// A node built at run time, outside the loaded schemas, still gets its keywords.
func TestKeywordsOfUnloadedNode(t *testing.T) {
	s := map[string]any{"type": "string", "pattern": "^a$", "description": "x"}
	if got := keywordsOf(s); got != kwType|kwPattern {
		t.Fatalf("keywords %b", got)
	}
	c := checker{}
	c.check(s, "b", "")
	if len(c.errs) != 1 {
		t.Fatalf("errors %v", c.errs)
	}
}

// Every bit belongs to one keyword name.
func TestKeywordBitsDistinct(t *testing.T) {
	seen := keywords(0)
	for name, bit := range keywordBits {
		if bit == 0 || bit&(bit-1) != 0 || seen&bit != 0 {
			t.Fatalf("%s has bit %b", name, bit)
		}
		seen |= bit
	}
	if seen != kwPropertyNames<<1-1 {
		t.Fatalf("bits %b leave a keyword without a name", seen)
	}
}
