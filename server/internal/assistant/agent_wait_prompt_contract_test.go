package assistant

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/mcptoolgolden"
)

// #10224 added inline observation without a prompt-size budget increase. Keep that total budget explicit while the
// per-piece baseline continues to be enforced and lowered through TestPromptSizeRatchet.
func TestAgentWaitPromptSizeBudget(t *testing.T) {
	const budget = 289184
	total := 0
	for _, size := range promptSizePieces(t) {
		total += size
	}
	if total > budget {
		t.Errorf("measured prompt size is %d bytes, over the %d-byte budget by %d; compact the wait_seconds guidance instead of raising the baseline", total, budget, total-budget)
	}
}

// TestAgentWaitPromptContract checks the meaning of the inline-observation guidance, not its current prose. The
// goldens hold the final tool list whose wire form TestGoldenMCPTools compares with the hub's actual tools.
func TestAgentWaitPromptContract(t *testing.T) {
	dir := filepath.Join("..", "hub", mcptoolgolden.Dir)
	path := filepath.Join(dir, "assistant") // for messages: the list's index and tool files
	raw, err := mcptoolgolden.Load(dir, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	var listed []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	tools := make(map[string]int, len(listed))
	for i, tool := range listed {
		if _, duplicate := tools[tool.Name]; duplicate {
			t.Fatalf("%s: duplicate tool %q", path, tool.Name)
		}
		tools[tool.Name] = i
	}

	for _, name := range []string{"agent_send", "agent_start", "task_plan"} {
		i, ok := tools[name]
		if !ok {
			t.Errorf("%s: tool %q is missing", path, name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			tool := listed[i]
			rawWait, ok := tool.InputSchema.Properties["wait_seconds"]
			if !ok {
				t.Fatal("wait_seconds is missing from input_schema.properties")
			}
			var wait struct {
				Type        string `json:"type"`
				Minimum     *int   `json:"minimum"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(rawWait, &wait); err != nil {
				t.Fatalf("wait_seconds: %v", err)
			}
			if wait.Type != "integer" {
				t.Errorf("wait_seconds type = %q, want integer", wait.Type)
			}
			if wait.Minimum == nil {
				t.Error("wait_seconds minimum is missing, want 0")
			} else if *wait.Minimum != 0 {
				t.Errorf("wait_seconds minimum = %d, want 0", *wait.Minimum)
			}
			for _, required := range tool.InputSchema.Required {
				if required == "wait_seconds" {
					t.Error("wait_seconds must be optional, not required")
				}
			}

			guidance := waitSecondsPromptGuidance(tool.Description)
			if guidance == "" {
				t.Fatal("tool description has no wait_seconds guidance paragraph")
			}
			// Health conversations use these tools without the shared assistant instructions. Keep the whole
			// contract in each tool rather than letting an unrelated shared variant satisfy these checks.
			allGuidance := guidance + "\n" + wait.Description

			requireWaitPromptClause(t, allGuidance, "omission and zero preserve the ordinary receipt",
				`omit(?:ted|ting)?|omission|absent`, `zero|\b0\b`, `receipt`, `ordinary|current|normal|usual|immediate|unchanged|only|keep|retain|preserve|return`)
			requireWaitPromptClause(t, allGuidance, "positive waits observe for at most 50 seconds after dispatch",
				`wait_seconds`, `observ\w*`, `at most|up to|capp?ed|max(?:imum)?\b`, `50\s*(?:seconds|s\b)`, `dispatch`)
			requireWaitPromptClause(t, allGuidance, "inline observation", `inline`, `observ\w*|result`)
			requireWaitPromptClause(t, allGuidance, "this input's completed reply", `completed`, `this\s+input(?:'s|s’)?`, `reply`)
			requireWaitPromptClause(t, allGuidance, "an existing question reference", `question`, `pending|existing`, `interaction_id|question reference`)
			for _, state := range []string{"timeout", "cancelled", "unavailable", "interrupted", "failed"} {
				requireWaitPromptClause(t, allGuidance, "the "+state+" result", `observ\w*|inline`, `\b`+state+`\b`)
			}
			requireWaitPromptClause(t, allGuidance, "known input and session references",
				`record|result|observ\w*`, `input`, `session`, `known|available`, `reference|correlat\w*|(?:^|\W)ids?(?:\W|$)`)

			for _, forbidden := range []struct {
				action   string
				patterns []string
			}{
				{"answering questions", []string{`answer\w*`, `question`}},
				{"accepting tasks", []string{`accept\w*`, `task`}},
				{"resending input", []string{`re-?send\w*|resend\w*`}},
				{"cancelling work", []string{`cancel\w*`}},
			} {
				requireWaitPromptForbiddenClause(t, allGuidance, forbidden.action, forbidden.patterns...)
			}
			requireWaitPromptClause(t, allGuidance, "normal supervision still applies",
				`supervis\w*`, `task_review`, `normal|usual|still|continu\w*|remain\w*|appl\w*`)
			requireWaitPromptClause(t, allGuidance, "older nodes time out rather than attribute an unrelated reply",
				`older|legacy`, `node`, `time\s*out|timeout`, `unrelated|another|wrong`, `reply`)
			if name == "task_plan" {
				requireWaitPromptClause(t, allGuidance, "only an immediately dispatchable single-task plan waits",
					`only`, `immediate`, `dispatch`, `single|one`, `task`, `wait`)
			}
		})
	}
}

// waitSecondsPromptGuidance starts at the wait_seconds paragraph so unrelated failures or replies earlier in a long
// tool description cannot satisfy the observation contract.
func waitSecondsPromptGuidance(description string) string {
	paragraphs := strings.Split(description, "\n\n")
	for i, paragraph := range paragraphs {
		if strings.Contains(paragraph, "wait_seconds") {
			return strings.Join(paragraphs[i:], "\n\n")
		}
	}
	return ""
}

var waitPromptClauseBoundary = regexp.MustCompile(`(?i)[.!?;](?:\s|$)|\n+|\b(?:but|however|except|unless)\b`)
var waitPromptNegation = regexp.MustCompile(`(?i)never|\bno\b|\bnot\b|without|must\s+not`)

// requireWaitPromptForbiddenClause keeps the negation before each forbidden action. Contrasts and semicolons end the
// clause, so one negated action cannot make a following contradictory action pass.
func requireWaitPromptForbiddenClause(t *testing.T, text, action string, patterns ...string) {
	t.Helper()
	for _, clause := range waitPromptClauseBoundary.Split(text, -1) {
		negation := waitPromptNegation.FindStringIndex(clause)
		if negation == nil {
			continue
		}
		matches := true
		for _, pattern := range patterns {
			location := regexp.MustCompile(`(?i)` + pattern).FindStringIndex(clause)
			if location == nil || location[0] < negation[0] {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	t.Errorf("guidance does not clearly negate %s before the action", action)
}

// requireWaitPromptClause permits compaction and reordering while keeping related ideas in the same sentence or line.
func requireWaitPromptClause(t *testing.T, text, contract string, patterns ...string) {
	t.Helper()
	for _, clause := range waitPromptClauseBoundary.Split(text, -1) {
		matches := true
		for _, pattern := range patterns {
			if !regexp.MustCompile(`(?i)` + pattern).MatchString(clause) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	t.Errorf("guidance does not express %s: want one clause matching %v", contract, patterns)
}
