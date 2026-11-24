package assistant

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestHealthIntakePageCategoryColors(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
	for _, tc := range []struct{ kind, color string }{{"water", "blue"}, {"protein", "amber"}} {
		for _, version := range []int{6, 9, 11} {
			for _, days := range []int{7, 30} {
				t.Run(fmt.Sprintf("%s/v%d/%d", tc.kind, version, days), func(t *testing.T) {
					state := fmt.Sprintf(`{"kind":%q,"days":%d}`, tc.kind, days)
					res := intakePageResult(t, intakePageHTTP(s, tc.kind, "", nil, state, version, "full"))
					tile := res.View.Sections[0].Items[0].Tiles[0]
					if tile.Color != tc.color {
						t.Fatalf("%s tile color: %+v", tc.kind, tile)
					}
					if version >= 9 {
						if tile.Gauge == nil || tile.Gauge.Color != tc.color || *tile.Gauge.Ratio != 0 {
							t.Fatalf("%s gauge: %+v", tc.kind, tile.Gauge)
						}
					} else if tile.Gauge != nil || !strings.Contains(tile.Subtitle, "0%") {
						t.Fatalf("legacy gauge did not retain percentage: %+v", tile)
					}
					chart := res.View.Sections[3].Items[1]
					if chart.Kind != "bar" || len(chart.Series) != 1 || chart.Series[0].Color != tc.color || chart.Series[0].Tone != "" {
						t.Fatalf("%s chart color: %+v", tc.kind, chart)
					}
				})
			}
		}
	}
}

func TestHealthIntakePageGoalKeepsCategoryColor(t *testing.T) {
	now := waterTestTime("2026-11-16T10:00:00Z")
	c := defaultHealthIntakeSettings()
	for _, tc := range []struct {
		kind, color string
		goal        int
	}{{"water", "blue", c.Water.Goal}, {"protein", "amber", c.Protein.Goal}} {
		for _, factor := range []float64{0.5, 1, 1.5} {
			t.Run(fmt.Sprintf("%s/%g", tc.kind, factor), func(t *testing.T) {
				amount := float64(tc.goal) * factor
				v := sumHealthIntake([]db.HealthIntakeRow{{Kind: tc.kind, Amount: amount, At: now}}, c, now, time.UTC, 7)
				page := drawIntakePage(intakePageState{Kind: tc.kind, Days: 7}, c, v, waterReminderState{}, now, time.UTC, "en", 11, false)
				tile := page.Sections[0].Items[0].Tiles[0]
				if tile.Color != tc.color || tile.Gauge.Color != tc.color || *tile.Gauge.Ratio != min(1.0, factor) || tile.Value != intakeNumber(amount) {
					t.Fatalf("goal progress: %+v", tile)
				}
				if factor >= 1 && tile.Subtitle != "Today's goal is met." {
					t.Fatalf("goal confirmation missing: %+v", tile)
				}
			})
		}
	}
}

func TestHealthIntakePageLegacyProgressTotals(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
	for _, kind := range []string{"water", "protein"} {
		for _, version := range []int{1, 4, 5} {
			t.Run(fmt.Sprintf("%s/v%d", kind, version), func(t *testing.T) {
				res := intakePageResult(t, intakePageHTTP(s, kind, "", nil, "", version, "full"))
				row := res.View.Sections[0].Items[0]
				unit := map[string]string{"water": "ml", "protein": "g"}[kind]
				if row.Type != "row" || row.Value != "0 "+unit || !strings.Contains(row.Subtitle, "0%") {
					t.Fatalf("legacy totals lost: %+v", row)
				}
			})
		}
	}
}

func TestHealthIntakePageTimezoneCopyByKind(t *testing.T) {
	now := waterTestTime("2026-11-16T10:00:00Z")
	c := defaultHealthIntakeSettings()
	v := sumHealthIntake(nil, c, now, time.UTC, 7)
	for _, tc := range []struct {
		lang           i18n.Lang
		water, protein string
	}{
		{"en", "Recorded and reminded in UTC time", "Recorded in UTC time"},
		{"zh-Hans", "按 UTC 时间提醒和记录", "按 UTC 时间记录"},  // i18n-ignore: localized view contract
		{"ja", "UTC の時刻で記録・通知します", "UTC の時刻で記録します"}, // i18n-ignore: localized view contract
	} {
		for _, kind := range []string{"water", "protein"} {
			t.Run(string(tc.lang)+"/"+kind, func(t *testing.T) {
				page := drawIntakePage(intakePageState{Kind: kind, Days: 7}, c, v, waterReminderState{}, now, time.UTC, tc.lang, 11, false)
				want := tc.water
				if kind == "protein" {
					want = tc.protein
				}
				if got := page.Sections[0].Items[1].Text; got != want {
					t.Fatalf("%s timezone copy: got %q, want %q", kind, got, want)
				}
			})
		}
	}
}
