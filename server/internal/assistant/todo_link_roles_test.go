package assistant

import "testing"

func TestReferenceReportLinks(t *testing.T) {
	own := "https://github.com/o/r/pull/1"
	foreign := "https://github.com/o/r/pull/10"
	other := "https://github.com/o/r/pull/11"
	raws := []string{own, foreign, other}
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{
			name: "related work line",
			text: "Done with the fix.\nRelated work: " + foreign + " tracks the upstream dependency.\nDelivery PR: " + own + "\n",
			want: []string{foreign},
		},
		{
			name: "background bullet",
			text: "- Background: " + foreign + "\nMerged our own change in " + own,
			want: []string{foreign},
		},
		{
			name: "not this delivery clause",
			text: foreign + " is related work, not this task's delivery. Our PR is " + own + ".",
			want: []string{foreign},
		},
		{
			name: "citation footer",
			text: "Shipped in " + own + ".\n<oai-mem-citation>\n<citation_entries>\nnotes:1|note=[see " + other + "]\n</citation_entries>\n</oai-mem-citation>",
			want: []string{other},
		},
		{
			name: "plain delivery report keeps delivery default",
			text: "Status: done\nPR: " + own + " and a follow-up " + foreign + " ready for review.",
			want: nil,
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := referenceReportLinks(tc.text, raws)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, u := range tc.want {
				if !got[u] {
					t.Fatalf("expected %s marked reference, got %v", u, got)
				}
			}
		})
	}
}
