package i18n

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// regexpFill is the regexp replace fill took over from; fill must give the same text for every input whose values
// do not end in a full stop (TestFillDropsAFilledSentencesFullStop).
var oldPlaceholder = regexp.MustCompile(`\{(\w+)\}`)

func regexpFill(s string, a Args) string {
	return oldPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		v, ok := a[m[1:len(m)-1]]
		if !ok {
			return m
		}
		switch v := v.(type) {
		case string:
			return v
		case int:
			return strconv.Itoa(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
		return fmt.Sprint(v)
	})
}

var fillArgs = Args{
	"name": "Ada", "count": 3, "n": int64(-7), "x": 1.5, "empty": "", "b": "{name}", "_": true, "a1": "日本",
	"long_name_9": []string{"p", "q"},
}

func TestFillSamples(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"no braces":              "no braces",
		"{name}":                 "Ada",
		"hi {name}, {count} new": "hi Ada, 3 new",
		"{n}{x}{empty}|":         "-71.5|",
		"{b}":                    "{name}", // a value is not filled again
		"{unknown} {name}":       "{unknown} Ada",
		"{{name}}":               "{Ada}",
		"{name":                  "{name",
		"{}{ name}{na me}":       "{}{ name}{na me}",
		"{名前}{name}":             "{名前}Ada",
		"{_}{a1}{long_name_9}":   "true日本[p q]",
	} {
		if got := fill(in, fillArgs); got != want {
			t.Errorf("fill(%q) = %q, want %q", in, got, want)
		}
	}
}

// fill matches the regexp version on random mixes of braces, ASCII words, non-ASCII letters and punctuation.
func TestFillMatchesRegexpRandom(t *testing.T) {
	frags := []string{"{", "}", "{{", "}}", "name", "count", "n", "x", "b", "_", "a1", "long_name_9", "unknown", "9",
		"Z", " ", "-", ".", "\n", "é", "名前", "数", "😀", "\xff", "{name}", "{count}", "{}"}
	r := rand.New(rand.NewPCG(1, 2))
	cases := 200000
	if testing.Short() {
		cases = 20000
	}
	for range cases {
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(frags[r.IntN(len(frags))])
		}
		s := b.String()
		if got, want := fill(s, fillArgs), regexpFill(s, fillArgs); got != want {
			t.Fatalf("fill(%q) = %q, regexp gives %q", s, got, want)
		}
	}
}

// fill matches the regexp version on every catalog text, with all its placeholders known and with none known.
func TestFillMatchesRegexpCatalogs(t *testing.T) {
	for l, cat := range catalogs() {
		for k, s := range cat {
			all := Args{}
			for _, m := range oldPlaceholder.FindAllStringSubmatch(s, -1) {
				all[m[1]] = "<" + m[1] + ">"
			}
			for _, a := range []Args{all, {"unrelated": 1}} {
				if got, want := fill(s, a), regexpFill(s, a); got != want {
					t.Errorf("%s %s: fill = %q, regexp gives %q", l, k, got, want)
				}
			}
		}
	}
}

// BenchmarkT is a hub hot path: one transcript line in a non-default language with one placeholder.
func BenchmarkT(b *testing.B) {
	args := Args{"prompt": "check the build and report back"}
	b.ReportAllocs()
	for b.Loop() {
		T(En, "hub-transcript_text.loop_fired_prompt", args)
	}
}

// Quoted source text counts punctuation and whitespace as part of the data.
func TestFillKeepsVerbatimSource(t *testing.T) {
	got := fill("{sender}: {quote}. ({reason}).", Args{"sender": Verbatim("A. "), "quote": Verbatim("原文。"), "reason": "Ready."})
	if want := "A. : 原文。. (Ready)."; got != want {
		t.Fatalf("source or sentence interpolation: got %q, want %q", got, want)
	}
}

// A whole sentence filled into a longer one (a node refusal as the reason) keeps one full stop, not "。），" (#6822).
func TestFillDropsAFilledSentencesFullStop(t *testing.T) {
	for _, c := range []struct{ in, v, want string }{
		{"这条没发出去（{v}），字还在输入框里", "开着。这次没有发送。", "这条没发出去（开着。这次没有发送），字还在输入框里"},
		{"不能开始：{v}。重试", "电脑没连上。 ", "不能开始：电脑没连上。重试"},
		{"Not sent ({v}).", "Nothing was sent.", "Not sent (Nothing was sent)."},
		{"{v}；{v}、{v}：{v}，", "好。", "好；好、好：好，"},
		// kept: before plain text, at the end, an ellipsis, another mark, no full stop
		{"{v} 再试", "没连上。", "没连上。 再试"},
		{"没发出去：{v}", "这次没有发送。", "没发出去：这次没有发送。"},
		{"（{v}）", "等一下...", "（等一下...）"},
		{"（{v}）", "真的吗？", "（真的吗？）"},
		{"（{v}）", "1.5", "（1.5）"},
		{"（{v}）", "", "（）"},
	} {
		if got := fill(c.in, Args{"v": c.v}); got != c.want {
			t.Errorf("fill(%q, %q) = %q, want %q", c.in, c.v, got, c.want)
		}
	}
}
