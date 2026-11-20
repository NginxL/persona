package assistant

import (
	"net/url"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
)

// botAnswerCards is how many cards one answer takes at most; the rest is in the app.
const botAnswerCards = 5

// answerCardMaxBytes bounds one complete answer card's JSON: Feishu refuses cards past ~30 KB; keep 2 KB margin.
const answerCardMaxBytes = 28000

// botAnswerCard is one card of an answer and what is sent instead when Feishu refuses it.
type botAnswerCard struct {
	card     *feishubot.Card
	fallback string
}

// answerCards lays out a final answer as the bot's cards (RenderAnswer: header, sections, tables, links, tag):
// process panel on the first when steps is not nil, foot under the last, a note when cut short. The panel is
// outside RenderAnswer's body budget: the first card is measured whole, panel steps halved to fit, dropped at
// zero (endHeader added at send fits the 2 KB margin). None for quiet.
// name names the button of a bare address in the answer (linkNamer; nil: by its host).
func answerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) []botAnswerCard {
	if quiet {
		return nil
	}
	// Charts (persona-card) become numbers over the whole answer before it is cut into cards: cut first, a chart
	// longer than one card would read as nothing and leave an empty card.
	a := feishubot.RenderAnswerWith(feishubot.PersonaCards(answer), botAnswerPiece, botAnswerCards, name)
	out := make([]botAnswerCard, len(a.Parts))
	for i, p := range a.Parts {
		build := func(panelSteps int) *feishubot.Card {
			c := p.Card(answer)
			if i == 0 && steps != nil {
				addProcessPanel(l, c, steps, panelSteps)
			}
			p.Fill(c)
			if i == len(a.Parts)-1 {
				if a.Clipped {
					c.Note(i18n.T(l, "assistant-feishu_cards.only_beginning"))
				}
				c.Note(foot)
			}
			return c
		}
		c := build(0)
		if i == 0 && steps != nil {
			for n := min(processPanelStepsMost, len(steps)); n > 0; n /= 2 {
				if tried := build(n); len(tried.JSON()) <= answerCardMaxBytes {
					c = tried
					break
				}
			}
		}
		out[i] = botAnswerCard{card: c, fallback: p.Text}
	}
	return out
}
