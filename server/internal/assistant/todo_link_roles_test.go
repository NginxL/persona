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
			// A bare "background" inside a delivery sentence must not demote the task's own PR (P1 review finding).
			name: "background worker delivery stays delivery",
			text: "Fixed the background sync worker in " + own + " and shipped it.",
			want: nil,
		},
		{
			// Explicit framing still marks the link reference, including the colon form and the "as background" form.
			name: "explicit background framing is reference",
			text: own + " is included as background only; our delivery is " + foreign + ".",
			want: []string{own},
		},
		{
			// Mid-sentence colon framing, not a line-start marker: only the link inside that sentence is reference.
			name: "background colon clause is reference",
			text: "See background: " + foreign + " for the upstream bug. Delivery landed in " + own + ".",
			want: []string{foreign},
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
