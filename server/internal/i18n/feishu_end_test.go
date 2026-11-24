package i18n

import "testing"

// These ending keys are built dynamically by the Lark card header and footer, so the static literal-key check
// cannot detect one missing from every catalog. Check each catalog directly as well as the real lookup.
func TestFeishuEndLabels(t *testing.T) {
	for _, tc := range []struct {
		lang Lang
		want string
	}{
		{En, "Ended: another request started"},
		{ZhHans, "已结束：开始处理其他请求"},
		{Ja, "別の依頼が始まったため終了"},
	} {
		t.Run(string(tc.lang), func(t *testing.T) {
			for _, end := range []string{"done", "stopped", "error", "gave_up", "gone", "offline", "recalled", "unrelated"} {
				key := "assistant-feishu.end_" + end
				local := catalog(tc.lang)[key]
				if local == "" || T(tc.lang, key) != local {
					t.Errorf("%s: catalog %q, lookup %q", key, local, T(tc.lang, key))
				}
			}
			if got := T(tc.lang, "assistant-feishu.end_unrelated"); got != tc.want {
				t.Errorf("unrelated ending = %q, want %q", got, tc.want)
			}
		})
	}
}
