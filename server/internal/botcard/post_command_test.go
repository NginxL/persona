package botcard

import "testing"

func TestPostCommandMentionBoundaries(t *testing.T) {
	mentions := []Mention{
		{Key: "@_user_1", Name: "Persona", ID: UserID{OpenID: "ou_bot", UserID: "u_bot", UnionID: "on_bot"}},
		{Key: "@_user_10", Name: "Persona", ID: UserID{OpenID: "ou_other"}},
	}
	isBot := func(m Mention) bool { return m.ID.OpenID == "ou_bot" }
	for _, tc := range []struct{ name, body, want string }{
		{"adjacent", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/read summarize"}]]}`, "/read summarize"},
		{"spaced", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":" /ai summarize"}]]}`, "/ai summarize"},
		{"open ID", `{"content":[[{"tag":"at","user_id":"ou_bot"},{"tag":"text","text":"/ai summarize"}]]}`, "/ai summarize"},
		{"user ID", `{"content":[[{"tag":"at","user_id":"u_bot"},{"tag":"text","text":"/ai summarize"}]]}`, "/ai summarize"},
		{"union ID", `{"content":[[{"tag":"at","user_id":"on_bot"},{"tag":"text","text":"/ai summarize"}]]}`, "/ai summarize"},
		{"localized", `{"en_us":{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/ai summarize"}]]}}`, "/ai summarize"},
		{"new paragraph", `{"content":[[],[{"tag":"text","text":"  "},{"tag":"at","user_id":"@_user_1"}],[{"tag":"text","text":"/ai summarize"}]]}`, "/ai summarize"},
		{"title is not a command", `{"title":"Title","content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/ai summarize"}]]}`, "**Title**\n@Persona/ai summarize"},
		{"same name different ID", `{"content":[[{"tag":"at","user_id":"@_user_10"},{"tag":"text","text":"/ai summarize"}]]}`, "@Persona/ai summarize"},
		{"same name before real bot", `{"content":[[{"tag":"at","user_id":"@_user_10"},{"tag":"text","text":" /ai summarize "},{"tag":"at","user_id":"@_user_1"}]]}`, "@Persona /ai summarize @Persona"},
		{"other mention after bot", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"at","user_id":"@_user_10"},{"tag":"text","text":"/ai summarize"}]]}`, "@Persona/ai summarize"},
		{"unresolved at", `{"content":[[{"tag":"at","user_id":"ou_unknown","user_name":"Persona"},{"tag":"text","text":"/ai summarize"}]]}`, "@Persona/ai summarize"},
		{"display name alone", `{"content":[[{"tag":"at","user_name":"Persona"},{"tag":"text","text":"/ai summarize"}]]}`, "@Persona/ai summarize"},
		{"literal name", `{"content":[[{"tag":"text","text":"@Persona /ai summarize "},{"tag":"at","user_id":"@_user_1"}]]}`, "@Persona /ai summarize @Persona"},
		{"literal key", `{"content":[[{"tag":"text","text":"@_user_1/ai summarize"}]]}`, "@Persona/ai summarize"},
		{"ordinary words", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"please /ai summarize"}]]}`, "please /ai summarize"},
		{"word boundary", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/aim summarize"}]]}`, "/aim summarize"},
		{"trailing words and media", `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/read summarize "},{"tag":"at","user_id":"@_user_10"}],[{"tag":"img","image_key":"img_example"}]]}`, "/read summarize @Persona\n[图片]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ParseContent("post", tc.body, mentions)
			before := c.Text
			if got := c.StripLeadingMentions(mentions, isBot); got != tc.want {
				t.Fatalf("got %q, want %q (display %q)", got, tc.want, c.Text)
			}
			if c.Text != before {
				t.Fatal("command extraction changed display text")
			}
			if got := c.StripLeadingMentions(mentions, isBot); got != tc.want {
				t.Fatalf("repeated extraction: %q", got)
			}
		})
	}
	c := ParseContent("post", `{"content":[[{"tag":"at","user_id":"@_user_10"},{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"/ai summarize"}]]}`, mentions)
	if got := c.StripLeadingMentions(mentions, nil); got != "/ai summarize" {
		t.Fatalf("all leading mentions: %q", got)
	}
}

func TestTextCommandMentionBoundariesStayWhole(t *testing.T) {
	mentions := []Mention{{Key: "@_user_1", Name: "Persona", ID: UserID{OpenID: "ou_bot"}}}
	for _, tc := range []struct{ input, want string }{
		{"@_user_1 /ai summarize", "/ai summarize"},
		{"@_user_1/ai summarize", "@Persona/ai summarize"},
		{"@_user_10 /ai summarize", "@_user_10 /ai summarize"},
		{"@PersonaOther /ai summarize", "@PersonaOther /ai summarize"},
		{"@Persona/ai summarize", "@Persona/ai summarize"},
		{"@Persona /reader summarize", "/reader summarize"},
	} {
		c := ParseContent("text", `{"text":"`+tc.input+`"}`, mentions)
		if got := c.StripLeadingMentions(mentions, nil); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}
