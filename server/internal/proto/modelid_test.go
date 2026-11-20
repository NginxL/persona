package proto

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidModelID(t *testing.T) {
	for _, ok := range []string{
		"opus", "glm-5", "vendor/model-x", "opus[1m]", "claude-opus-4-6[1m]", "qwen3:32b", "claude-x@20250101",
		"gpt-5.1-codex", "meta-llama/Llama-3.1-8B-Instruct", "a_b", "7b",
		"~anthropic/claude-opus-latest", "~anthropic/claude-sonnet-latest[1m]", "~openai/gpt-mini-latest", "~x",
		"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-x-v1:0",
		strings.Repeat("a", MaxModelIDLen),
	} {
		if !ValidModelID(ok) {
			t.Errorf("ValidModelID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", " ", "opus ", " opus", "a b", "a\tb", "a\nb", "a\rb", "a\x00b", "a\x7fb",
		"-x", "--dangerously-skip-permissions", ".hidden", "/abs", "[1m]", "_x",
		"$(id)", "a$b", "a`id`", "a'b", `a"b`, `a\b`, "a;b", "a|b", "a&b", "a>b", "a<b", "a*", "a?",
		"~", "~~x", "~-x", "~/x", "~.x", "~ x", "a~b", "x~",
		"a,b", "a=b", "a+b", "a#b", "a(b)", "a{b}", "a!b", "a%b", "模型", "é",
		strings.Repeat("a", MaxModelIDLen+1),
	} {
		if ValidModelID(bad) {
			t.Errorf("ValidModelID(%q) = true, want false", bad)
		}
	}
}

// The rule as the assistant's tools say it has the same bound.
func TestModelIDRuleText(t *testing.T) {
	if !strings.Contains(ModelIDRule, strconv.Itoa(MaxModelIDLen)) || !strings.Contains(ModelIDRule, "~") {
		t.Fatalf("ModelIDRule: %s", ModelIDRule)
	}
}

func TestTrimModelWindow(t *testing.T) {
	for in, want := range map[string]string{
		"  claude-opus-5-5  ":  "claude-opus-5-5",
		"claude-opus-5-5[1m]":  "claude-opus-5-5",
		"claude-opus-5-5[1M]":  "claude-opus-5-5",
		"gemini-4-carbon":      "gemini-4-carbon",
		"gemini-4-carbon[1m] ": "gemini-4-carbon",
		"":                     "",
		"   ":                  "",
		"[1m]":                 "",
	} {
		if got := TrimModelWindow(in); got != want {
			t.Errorf("TrimModelWindow(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSessionModel(t *testing.T) {
	const carbon = "gemini-4-carbon"
	for _, c := range []struct {
		name, raw string
		declared  []string
		want      string
	}{
		{"empty", "", []string{carbon}, ""},
		{"blank", "   ", []string{carbon}, ""},
		{"placeholder", "<synthetic>", []string{carbon}, ""},
		{"nothing declared keeps the raw id", "gateway/echo", nil, "gateway/echo"},
		{"trims the raw id", "  " + carbon + " ", []string{carbon}, carbon},
		{"exact hit adopts the declared spelling", "Gemini-4-Carbon", []string{carbon}, carbon},
		{"whole id before any suffix", "group/auto-large", []string{"group/auto-large", "auto/large"}, "group/auto-large"},
		{"gateway prefix folds to the clean id", "passthrough/route-gemini-4-carbon/" + carbon, []string{carbon}, carbon},
		{"declared id with a slash wins as the longer suffix", "passthrough/vendor/model", []string{"vendor/model", "model"}, "vendor/model"},
		{"a plain echo does not gain [1m]", carbon, []string{carbon + "[1m]"}, carbon},
		{"a [1m] echo keeps it on the clean base", carbon + "[1m]", []string{carbon}, carbon + "[1m]"},
		{"a prefixed [1m] echo keeps it", "passthrough/x/" + carbon + "[1m]", []string{carbon}, carbon + "[1m]"},
		{"no match keeps the whole id", "group/auto-large", []string{"auto/large"}, "group/auto-large"},
		{"no match on a bare suffix keeps it", "gateway/echo", []string{"m1"}, "gateway/echo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeSessionModel(c.raw, c.declared); got != c.want {
				t.Fatalf("NormalizeSessionModel(%q, %v) = %q, want %q", c.raw, c.declared, got, c.want)
			}
		})
	}
}

func TestSameLauncherModel(t *testing.T) {
	const opus = "claude-opus-5-5"
	for _, c := range []struct {
		name, reported, declared string
		want                     bool
	}{
		{"same id", opus, opus, true},
		{"ignores case", "Claude-Opus-5-5", opus, true},
		{"ignores the 1m marker", opus + "[1m]", opus, true},
		{"gateway prefix before the model", "passthrough/route-a/gemini-4-carbon", "gemini-4-carbon", true},
		{"gateway prefix against the 1m variant", "passthrough/route-a/gemini-4-carbon", "gemini-4-carbon[1m]", true},
		{"launcher's vendored id against the bare echo", opus, "route-a/" + opus, true},
		{"unrelated ids", "gw-small", "gw-gone", false},
		{"a slash alone is no match", "group/auto-large", "auto/large", false},
		{"empty on either side", "", opus, false},
		{"a placeholder on either side", "<synthetic>", opus, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SameLauncherModel(c.reported, c.declared); got != c.want {
				t.Fatalf("SameLauncherModel(%q, %q) = %v, want %v", c.reported, c.declared, got, c.want)
			}
		})
	}
}
