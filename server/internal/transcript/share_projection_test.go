package transcript

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
)

func TestShareSanitizerBeforeDisplayClipping(t *testing.T) {
	secret := "PRIVATE-BLOCK-SECRET"
	input := strings.Repeat("x", ShareDisplayLimit-40) + "-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat(secret, 4000)
	sanitized, err := SanitizeShareText(input)
	if err != nil {
		t.Fatal(err)
	}
	shown := ShareDisplayText(sanitized)
	if strings.Contains(shown, secret) || strings.Contains(shown, "BEGIN RSA") {
		t.Fatal("private-key block escaped redaction before clipping")
	}
	for _, text := range []string{"Authorization: Bearer synthetic-secret", `password="synthetic-password"`, "https://me:synthetic-password@example.test/?token=synthetic-token", "sk-abcdefghijklmnopqrstuvwxyz123456"} {
		clean, err := SanitizeShareText(text)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(clean, "synthetic-secret") || strings.Contains(clean, "synthetic-password") || strings.Contains(clean, "synthetic-token") || strings.Contains(clean, "sk-abcdef") {
			t.Fatalf("credential retained: %s", clean)
		}
	}
	raw := json.RawMessage(`{"outer":{"password":"synthetic-secret","text":"Authorization: Bearer synthetic-token"}}`)
	clean, err := SanitizeShareArguments(raw)
	if err != nil || strings.Contains(clean, "synthetic-secret") || strings.Contains(clean, "synthetic-token") {
		t.Fatalf("arguments: %s %v", clean, err)
	}
	if _, err := SanitizeShareText(strings.Repeat("x", ShareMessageLimit+1)); err == nil {
		t.Fatal("oversized component accepted")
	}
}
func shareSyntheticHistory() []byte {
	return []byte(`{"type":"user","uuid":"u1","origin":{"kind":"human"},"message":{"role":"user","content":"Synthetic input"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"text","text":"Synthetic narration"},{"type":"tool_use","id":"t1","name":"Read","input":{"password":"synthetic-secret","content":"Synthetic argument"}}]}}
{"type":"user","uuid":"r1","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Synthetic result"}]}}
{"type":"assistant","uuid":"a2","parentUuid":"r1","message":{"role":"assistant","content":[{"type":"text","text":"Synthetic final answer"}]}}
{"type":"system","subtype":"turn_duration","uuid":"end1","parentUuid":"a2"}
`)
}
func TestShareProjectionCompleteUnclippedAndProvenance(t *testing.T) {
	data := shareSyntheticHistory()
	out, err := ProjectShareBytes(context.Background(), data, "synthetic-source", "synthetic-incarnation", nil)
	if err != nil || !out.Complete || len(out.Messages) != 2 {
		t.Fatalf("projection: %+v %v", out, err)
	}
	answer := out.Messages[1].Message
	if answer.Text != "Synthetic final answer" || len(answer.Execution) != 2 {
		t.Fatalf("answer: %+v", answer)
	}
	if strings.Contains(answer.Execution[0].Arguments, "synthetic-secret") {
		t.Fatal("tool secret leaked")
	}
	unknown := strings.Replace(string(data), `"origin":{"kind":"human"},`, "", 1)
	omitted, err := ProjectShareBytes(context.Background(), []byte(unknown), "synthetic-source", "synthetic-incarnation", nil)
	if err != nil || len(omitted.Messages) != 1 || omitted.Omitted != 1 {
		t.Fatalf("unknown provenance: %+v %v", omitted, err)
	}
	peer := strings.Replace(string(data), `"kind":"human"`, `"kind":"peer"`, 1)
	peerOut, _ := ProjectShareBytes(context.Background(), []byte(peer), "synthetic-source", "synthetic-incarnation", nil)
	if len(peerOut.Messages) != 1 {
		t.Fatal("peer promoted to human input")
	}
}
func TestShareProjectionEOFAndRewriteFrontiers(t *testing.T) {
	ctx := context.Background()
	base := shareSyntheticHistory()
	partial := []byte(`{"type":"user","uuid":"u2","origin":{"kind":"human"},"message":{"role":"user","content":"Old partial`)
	observed := append(append([]byte{}, base...), partial...)
	first, err := ProjectShareBytes(ctx, observed, "source", "incarnation", nil)
	if err != nil || !first.Complete {
		t.Fatal(err, first.Reason)
	}
	appended := append(append([]byte{}, observed...), []byte(" row\"}}\n")...)
	next, err := ProjectShareBytes(ctx, appended, "source", "incarnation", &first.Frontier)
	if err != nil || !next.Complete {
		t.Fatal(err, next.Reason)
	}
	last := next.Messages[len(next.Messages)-1]
	if last.FirstSeen >= first.Frontier.EOF {
		t.Fatal("old partial row became future content")
	}
	rewrite := append([]byte{}, appended...)
	rewrite[20] = 'X'
	failed, err := ProjectShareBytes(ctx, rewrite, "source", "incarnation", &next.Frontier)
	if err != nil || failed.Complete || failed.Reason != "source_changed" {
		t.Fatal("same-length rewrite accepted")
	}
	failed, _ = ProjectShareBytes(ctx, appended, "replacement", "incarnation", &next.Frontier)
	if failed.Complete {
		t.Fatal("source replacement accepted")
	}
}
func TestSharePrivateKeyAcrossAssistantSegments(t *testing.T) {
	data := strings.Replace(string(shareSyntheticHistory()), "Synthetic narration", "-----BEGIN RSA PRIVATE KEY-----", 1)
	data = strings.Replace(data, "Synthetic final answer", "SyntheticSecretSuffix", 1)
	out, err := ProjectShareBytes(context.Background(), []byte(data), "synthetic-source", "synthetic-incarnation", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out.Messages)
	if strings.Contains(string(raw), "SyntheticSecretSuffix") {
		t.Fatal("cross-segment private-key suffix published")
	}
}

func TestShareCodexCompleteEventsAndFrontier(t *testing.T) {
	data := []byte(`{"type":"session_meta","payload":{"id":"synthetic-thread"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"synthetic-turn"}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"Ambiguous input"}]}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"text","text":"Synthetic result"}]}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"synthetic-turn"}}
`)
	out, err := ProjectShareBytes(context.Background(), data, "synthetic-source", "synthetic-incarnation", nil)
	if err != nil || !out.Complete || len(out.Messages) != 1 || out.Messages[0].Message.Text != "Synthetic result" || out.Omitted != 1 {
		t.Fatalf("Codex complete events: %+v %v", out, err)
	}
	rewritten := append([]byte(nil), data...)
	rewritten[10] = 'X'
	bad, _ := ProjectShareBytes(context.Background(), rewritten, "synthetic-source", "synthetic-incarnation", &out.Frontier)
	if bad.Complete {
		t.Fatal("Codex rewrite accepted")
	}
}

func TestShareCodexMcpTextOnly(t *testing.T) {
	data := []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"mcp","server":"example","tool":"lookup","arguments":{},"result":{"content":[{"type":"text","text":"Synthetic result"},{"type":"image","data":"SyntheticImageBytes","mimeType":"image/png"}],"rawPayload":"SyntheticRawPayload"}}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"answer","content":[{"type":"text","text":"Done"}]}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}
`)
	out, err := ProjectShareBytes(context.Background(), data, "synthetic-source", "synthetic-incarnation", nil)
	if err != nil || !out.Complete || len(out.Messages) != 1 {
		t.Fatalf("share projection: %+v %v", out, err)
	}
	work := out.Messages[0].Message.Execution
	if len(work) != 1 || len(work[0].Results) != 1 || work[0].Results[0].Text != "Synthetic result" {
		t.Fatalf("MCP text export: %+v", work)
	}
	encoded, err := json.Marshal(out.Messages)
	if err != nil || strings.Contains(string(encoded), "SyntheticImageBytes") || strings.Contains(string(encoded), "SyntheticRawPayload") {
		t.Fatalf("image or raw payload exported: %s %v", encoded, err)
	}
}

func TestShareCodexRejectsWrongResultTypes(t *testing.T) {
	for _, test := range []struct {
		name string
		item string
	}{
		{"MCP string", `{"type":"McpToolCall","id":"mcp","result":"opaque"}`},
		{"MCP wrong shape", `{"type":"McpToolCall","id":"mcp","result":{"content":"bad"}}`},
		{"generated string", `{"type":"Extension","kind":"image_gen.generation","id":"generated","savedPath":"/home/example/picture.png","result":"<base64>"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}
{"type":"event_msg","payload":{"type":"item_completed","item":` + test.item + `}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"answer","content":[{"type":"text","text":"Done"}]}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}
`)
			out, err := ProjectShareBytes(context.Background(), data, "synthetic-source", "synthetic-incarnation", nil)
			if err != nil || out.Complete || out.Reason != "source_incomplete" || len(out.Messages) != 0 {
				t.Fatalf("malformed rollout published: %+v %v", out, err)
			}
		})
	}
}

func TestSharePendingCompletionRetainsFirstSeen(t *testing.T) {
	ctx := context.Background()
	pending := []byte(`{"type":"assistant","uuid":"pending","message":{"role":"assistant","content":[{"type":"text","text":"Pending result"}]}}
`)
	first, _ := ProjectShareBytes(ctx, pending, "source", "incarnation", nil)
	if len(first.Messages) != 0 {
		t.Fatal("pending result published")
	}
	next, _ := ProjectShareBytes(ctx, append(pending, []byte(`{"type":"system","subtype":"turn_duration","uuid":"end","parentUuid":"pending"}`+"\n")...), "source", "incarnation", &first.Frontier)
	if len(next.Messages) != 1 || next.Messages[0].FirstSeen != 0 {
		t.Fatal("completion used completion position")
	}
	var _ *devicewire.ShareFrontier = &next.Frontier
}

func TestShareSanitizerCommonCredentialFormats(t *testing.T) {
	// Synthetic values only: each must lose its secret part, keeping the name.
	leaks := map[string]string{
		"OPENAI_API_KEY=abc123def456ghi789":                                                  "abc123def456ghi789",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEX":                               "wJalrXUtnFEMIK7MDENGbPxRfiCYEX",
		"DB_PASSWORD=hunter2hunter2":                                                         "hunter2hunter2",
		"export PGPASSWORD=s3cretvalue":                                                      "s3cretvalue",
		"GITHUB_TOKEN=plain-looking-token-value":                                             "plain-looking-token-value",
		"token: tok_live_synthetic_value":                                                    "tok_live_synthetic_value",
		"client_secret=synthetic-client-secret":                                              "synthetic-client-secret",
		"Authorization: Basic dXNlcjpzeW50aGV0aWM=":                                          "dXNlcjpzeW50aGV0aWM=",
		"password: correct horse battery staple":                                             "battery staple",
		"postgres://app:Sup3rS3cret@db.example.test/app":                                     "Sup3rS3cret",
		"redis://:redispass@cache.example.test:6379":                                         "redispass",
		"slack xoxb-1234567890-synthetic-value":                                              "xoxb-1234567890",
		"https://bucket.example.test/o?X-Amz-Signature=abcdef0123&X-Amz-Credential=AKID":     "abcdef0123",
		"curl -u admin:synthetic-pw https://api.example.test":                                "synthetic-pw",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQOYBF\n-----END PGP PRIVATE KEY BLOCK-----": "lQOYBF",
	}
	for in, secret := range leaks {
		out, err := SanitizeShareText(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, secret) {
			t.Errorf("leaked %q in %q", secret, out)
		}
	}
	for _, keep := range []string{"max_tokens=4096", "We used 1200 tokens today.", "The author: Synthetic Writer"} {
		if out, _ := SanitizeShareText(keep); out != keep {
			t.Errorf("over-redacted %q to %q", keep, out)
		}
	}
	args, err := SanitizeShareArguments(json.RawMessage(`{"env":{"OPENAI_API_KEY":"sk-proj-syntheticvalue0001","PATH":"/usr/bin"},"headers":{"X-Auth-Token":"opaque-synthetic"},"client_secret":"cs-synthetic","max_tokens":1000}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-proj-syntheticvalue0001", "opaque-synthetic", "cs-synthetic"} {
		if strings.Contains(args, secret) {
			t.Errorf("argument leaked %q: %s", secret, args)
		}
	}
	if !strings.Contains(args, `"max_tokens":1000`) || !strings.Contains(args, "/usr/bin") {
		t.Errorf("argument over-redacted: %s", args)
	}
}
