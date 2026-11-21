package feishubot

import "testing"

func TestReadCommand(t *testing.T) {
	for _, c := range []struct {
		in, rest string
		ok       bool
	}{
		{"/read what broke", "what broke", true},
		{"/read", "", true},
		{"  /READ\n近两天的告警", "近两天的告警", true}, // i18n-ignore: an owner's words
		{"／read 看看", "看看", true},            // i18n-ignore: the full-width slash and an owner's words
		{"/读群 完整读取后总结", "完整读取后总结", true},    // i18n-ignore: the alias and an owner's words
		{"/读群", "", true},                   // i18n-ignore: the alias
		{"/reader", "", false},
		{"/读群聊", "", false}, // i18n-ignore: not the alias
		{"read this", "", false},
		{"/ai read", "", false},
		{"/", "", false},
		{"", "", false},
	} {
		rest, ok := ReadCommand(c.in)
		if rest != c.rest || ok != c.ok {
			t.Errorf("ReadCommand(%q) = %q, %v; want %q, %v", c.in, rest, ok, c.rest, c.ok)
		}
	}
}
