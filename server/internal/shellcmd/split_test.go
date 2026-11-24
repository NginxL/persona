package shellcmd

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want []Segment
		ok   bool
	}{
		{`A="x y" claude --model 'm 1' # note`, []Segment{{Words: []string{"A=x y", "claude", "--model", "m 1"}, Text: `A="x y" claude --model 'm 1' # note`}}, true},
		{"a \\\n  b; c && d | e", []Segment{
			{Words: []string{"a", "b"}, Text: "a \\\n  b"}, {Words: []string{"c"}, Text: "c"}, {Words: []string{"d"}, Text: "d"},
			{Words: []string{"e"}, Text: "e"},
		}, true},
		{`echo "$(id)" > /tmp/x 2>&1`, []Segment{{Words: []string{"echo", "$(id)"}, Text: `echo "$(id)" > /tmp/x 2>&1`, Subst: true,
			ArgSubst: true, Nested: []string{"id"}, Redirect: ">/tmp/x 2>&1"}}, true},
		// A redirection's target ends before the blanks after it, the ones only Unicode calls blanks too.
		{"x >a\v >b\u00a0", []Segment{{Words: []string{"x"}, Text: "x >a\v >b", Redirect: ">a >b"}}, true},
		{`echo 'open`, nil, false},
		{`echo $(printf ")`, nil, false},
		{`(cd x)`, nil, false},
		{`cat <<EOF`, nil, false},
		{`echo $(printf ")") hi`, []Segment{{Words: []string{"echo", `$(printf ")")`, "hi"}, Text: `echo $(printf ")") hi`,
			Subst: true, ArgSubst: true, Nested: []string{`printf ")"`}}}, true},
		{`echo "$(printf ")")"`, []Segment{{Words: []string{"echo", `$(printf ")")`}, Text: `echo "$(printf ")")"`,
			Subst: true, ArgSubst: true, Nested: []string{`printf ")"`}}}, true},
		{`echo $(printf ")"; send-mail --to alice) # "`, []Segment{{Words: []string{"echo", `$(printf ")"; send-mail --to alice)`},
			Text: `echo $(printf ")"; send-mail --to alice) # "`, Subst: true, ArgSubst: true,
			Nested: []string{`printf ")"; send-mail --to alice`}}}, true},
		{`echo $(printf "%s" "$(send-mail)")`, []Segment{{Words: []string{"echo", `$(printf "%s" "$(send-mail)")`},
			Text: `echo $(printf "%s" "$(send-mail)")`, Subst: true, ArgSubst: true,
			Nested: []string{"send-mail", `printf "%s" "$(send-mail)"`}}}, true},
		{"echo $(\n# )\nsend-mail\n)", []Segment{{Words: []string{"echo", "$(\n# )\nsend-mail\n)"},
			Text: "echo $(\n# )\nsend-mail\n)", Subst: true, ArgSubst: true,
			Nested: []string{"\n# )\nsend-mail\n"}}}, true},
	} {
		got, ok := Split(c.cmd)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v %v, want %+v %v", c.cmd, got, ok, c.want, c.ok)
		}
	}
}
