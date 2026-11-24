package subscriptions

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestCheckRecipeRejectsInvalidAllFieldPaths(t *testing.T) {
	setters := []struct {
		name string
		set  func(*connector.SubscriptionFields, string)
	}{
		{"sender_id", func(f *connector.SubscriptionFields, v string) { f.SenderID = v }},
		{"chat", func(f *connector.SubscriptionFields, v string) { f.Chat = v }},
		{"chat_name", func(f *connector.SubscriptionFields, v string) { f.ChatName = v }},
		{"seen", func(f *connector.SubscriptionFields, v string) { f.Seen = v }},
		{"reply_to", func(f *connector.SubscriptionFields, v string) { f.ReplyTo = v }},
	}
	for _, tc := range setters {
		for _, bad := range []string{"sender_id", "$.bad..path", "$[-1]", "$[9999999999999999999999999]"} {
			r := itemsRecipe()
			tc.set(r.Fields, bad)
			p := checkRecipe(r)
			if p == nil || p.Code != CodeBadRecipe {
				t.Errorf("%s=%q: checkRecipe = %+v, want %s", tc.name, bad, p, CodeBadRecipe)
			}
		}
	}
}

func TestSeenValueRejectsNonFiniteStrings(t *testing.T) {
	for _, s := range []string{"NaN", "nan", "+NaN", "-NaN", "Inf", "+Inf", "-Inf", "Infinity", "-Infinity"} {
		if seenValue(s) {
			t.Errorf("seenValue(%q) = true, want false", s)
		}
	}
}

func TestCallProblemBoundsRateLimitRetryAfter(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{`{"retry_after":12.5}`, 12500 * time.Millisecond},
		{`{"retry_after":0}`, time.Minute},
		{`{"retry_after":-5}`, time.Minute},
		{`{"retry_after":-1e300}`, time.Minute},
		{`{"retry_after":1e20}`, 24 * time.Hour},
		{`{"retry_after":1e300}`, 24 * time.Hour},
	}
	for _, tc := range cases {
		err := &rpc.Error{
			Code: wire.ConnectorErrorCodeRateLimited,
			Data: json.RawMessage(tc.raw),
		}
		prob, wait, connErr := callProblem(err)
		if prob == nil || prob.Code != CodeRateLimited || connErr {
			t.Fatalf("%s: callProblem = (%+v, %v, %v)", tc.raw, prob, wait, connErr)
		}
		if wait != tc.want {
			t.Errorf("%s: wait = %v, want %v", tc.raw, wait, tc.want)
		}
	}
}
