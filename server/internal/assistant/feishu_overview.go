package assistant

// /tasks overview: pending decisions and open todos; read-only while the fleet is online, one feishu.status call.

import (
	"context"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/transcript"
)

type overviewCard struct {
	card     *feishubot.Card
	fallback string
}

func (s *Service) tasksOverview(ctx context.Context) []overviewCard {
	l, web := s.botLang(ctx), s.webBase(ctx)
	var es []reportEntry
	labels := map[int]bool{}
	add := func(md string, bs []feishubot.Button) {
		es = append(es, reportEntry{md: md, buttons: bs, bytes: feishuElBytes(func(c *feishubot.Card) {
			c.Markdown(md)
			c.Buttons(bs...)
		})})
	}
	label := func(key string) {
		add("**"+i18n.T(l, key)+"**", nil)
		labels[len(es)-1] = true
	}

	label("assistant-reports.decide")
	if ixs := s.visiblePendingInteractions(); len(ixs) > 0 {
		for _, ix := range ixs {
			where := transcript.Title(ix.SessionTitle)
			if where == "" {
				where = ix.TaskTitle
				if where == "" {
					where = "Persona"
				}
			}
			head := "**" + feishubot.EscapeMarkdown(where) + "**"
			if m := s.machineName(ix.MachineID); m != "" {
				head += " · " + feishubot.EscapeMarkdown(m)
			}
			link := web
			if ix.SessionID != "" {
				link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
			}
			var bs []feishubot.Button
			if b := s.openLinkButton(ctx, link); b != nil { // nil for an empty or scheme-less web base
				bs = []feishubot.Button{b}
			}
			add(head+"\n"+feishubot.EscapeMarkdown(interactionQuestion(l, ix)), bs)
		}
	} else {
		add(i18n.T(l, "assistant-feishu_cards.tasks_no_decide"), nil)
	}

	label("assistant-feishu_cards.tasks_todos")
	s.overviewTodos(ctx, l, web, add)
	return packOverview(l, es, labels)
}

// overviewTodos appends one row per open todo, oldest first (ties by id), including manual todos that no working
// session is bound to: todoReport's links only enumerate session-bound todos, so the open set is read straight from
// the book under its lock. Session/Agent links are then supplemented from the report.
func (s *Service) overviewTodos(ctx context.Context, l i18n.Lang, web string, add func(md string, bs []feishubot.Button)) {
	sessions := map[string][][2]string{}
	if r := s.todoReport(ctx); r != nil {
		seen := map[[2]string]bool{}
		for _, ln := range r.Links {
			if k := [2]string{ln.MachineID, ln.SessionID}; ln.SessionID != "" && !seen[k] {
				seen[k] = true
				sessions[ln.TodoID] = append(sessions[ln.TodoID], k)
			}
		}
	}
	var open []Todo
	if s.ensureTodosLoaded(ctx) == nil {
		s.todos.mu.RLock()
		for _, t := range s.todos.todos {
			if t.DoneAt == nil {
				open = append(open, copyTodoLocked(t)) // copied in-lock; todoNow/todoReport take the lock themselves
			}
		}
		s.todos.mu.RUnlock()
		sort.Slice(open, func(i, j int) bool {
			if !open[i].CreatedAt.Equal(open[j].CreatedAt) {
				return open[i].CreatedAt.Before(open[j].CreatedAt)
			}
			return open[i].ID < open[j].ID
		})
	}
	if len(open) == 0 {
		add(i18n.T(l, "assistant-feishu_cards.tasks_no_todos"), nil)
		return
	}
	for _, t := range open {
		var bs []feishubot.Button
		btn := func(key, u string, page bool) {
			if b := safeLinkButton(i18n.T(l, key), u); b != nil { // nil for an empty/non-http(s) URL
				if page { // a page of the web app: Feishu's sidebar on the desktop
					b = feishubot.PageBtn(i18n.T(l, key), u)
				}
				bs = append(bs, b)
			}
		}
		btn("assistant-reports.open_task", todoLink(web, t.ID), true)
		btn("assistant-reports.open_pr", t.PRURL, false)
		for _, x := range sessions[t.ID] {
			btn("assistant-reports.open_agent", sessionLink(web, x[0], x[1], false), true)
		}
		md := "**" + feishubot.EscapeMarkdown(clip(oneLine(t.Title), 40)) + "**"
		if t.StageKind != "general" { // general work has no strip, as on the other cards and in the apps (#11590)
			md += "\n" + stageBar(l, t.Stage, false) // open work: never done
		}
		add(md, bs)
	}
}

func (s *Service) sessionAssistantLocal(machine, session string) bool {
	m, ok := s.fleetMachine(machine)
	sess, found := s.nodeSession(machine, session)
	return ok && found && m.isAssistant(sess)
}

// packOverview splits entries with the reports' greedy packer; labels get no divider, each card has a fallback.
func packOverview(l i18n.Lang, es []reportEntry, labels map[int]bool) []overviewCard {
	title := i18n.T(l, "assistant-feishu_cards.tasks_overview")
	part := i18n.T(l, "assistant-reports.part", i18n.Args{"k": 99, "n": 99})
	head := func(md string) int {
		return len(feishubot.NewCard(title+part, feishubot.Blue, title).Markdown(md).JSON())
	}
	// Byte reservation: card 1 may carry both section labels, later cards at most one.
	segs := reportCardSegments(es, head("**x**\n\n**x**"), head("**x**"), feishuElBytes(func(c *feishubot.Card) { c.HR() }))
	cards := make([]overviewCard, 0, len(segs))
	off := 0
	for k, seg := range segs {
		cardTitle := title
		if len(segs) > 1 {
			cardTitle += i18n.T(l, "assistant-reports.part", i18n.Args{"k": k + 1, "n": len(segs)})
		}
		c := feishubot.NewCard(cardTitle, feishubot.Blue, cardTitle)
		lines := make([]string, 0, len(seg))
		for j, e := range seg {
			gi := off + j
			if j > 0 && !labels[gi] && !labels[gi-1] {
				c.HR()
			}
			c.Markdown(e.md)
			c.Buttons(e.buttons...)
			line := fallbackText.Replace(e.md)
			if !labels[gi] {
				line = "- " + line
			}
			lines = append(lines, line)
		}
		off += len(seg)
		cards = append(cards, overviewCard{c, cardTitle + "\n" + clip(strings.Join(lines, "\n"), reportFallbackMax)})
	}
	return cards
}
