package iphonectl

import (
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Sensitivity is a kind of step the owner approves before it is done.
type Sensitivity string

const (
	Payment     Sensitivity = "payment"
	Deletion    Sensitivity = "delete"
	SendMessage Sensitivity = "send_message"
	Password    Sensitivity = "password"
	// Confirm is the owner's setting to be asked before any input, not a property of the step.
	Confirm Sensitivity = "confirm"
	// Unchecked: the phone gives no element tree, so the rules can't see what an input hits; the owner approves
	// every input, each on its own.
	Unchecked Sensitivity = "unchecked"
)

// Rules say which steps need the owner's approval and which are never done.
//
// The automation can never approve itself: approval comes only with the call that resumes the pending step, which the
// hub makes after the owner answered on another screen, never from the steps; and no step touches or types into
// Persona's own app on the phone (SelfApps), where approval cards and their buttons appear.
type Rules struct {
	// SelfApps are the bundle ids of Persona's apps on the phone (from config; app identities never appear in code).
	SelfApps []string
	// SelfNames are those apps' names (the whole name, case aside): a front app with one of them is Persona's
	// whatever its bundle id, so the rule holds while no bundle id is known.
	SelfNames []string
	// Words add labels, per kind, to the built-in ones (lowercase; a label matches when it is the phrase or starts
	// with it).
	Words map[Sensitivity][]string
	// MessagingApps and PaymentApps add bundle ids to the built-in ones.
	MessagingApps []string
	PaymentApps   []string
}

// Verdict is what the rules say about one step.
type Verdict struct {
	Needs []Sensitivity `json:"needs,omitempty"` // approve these first; empty: go ahead
	// Refused is why the step is never done, empty when it may be.
	Refused  string   `json:"refused,omitempty"`
	Evidence []string `json:"evidence,omitempty"` // the facts the verdict rests on
}

func (v *Verdict) need(k Sensitivity, evidence string) {
	if !slices.Contains(v.Needs, k) {
		v.Needs = append(v.Needs, k)
	}
	if !slices.Contains(v.Evidence, evidence) {
		v.Evidence = append(v.Evidence, evidence)
	}
}

// Built-in labels per kind, lowercase: a label matches when it is the phrase or starts with it (followed by a space
// or punctuation for Latin script). They are the words iOS and common apps put on their buttons, so they stay in
// their languages.
var builtinWords = map[Sensitivity][]string{
	Payment: {
		"pay", "pay now", "buy", "buy now", "purchase", "subscribe", "checkout", "check out", "place order",
		"confirm payment", "confirm and pay", "confirm purchase", "apple pay", "double click to pay", "send money",
		"支付", "付款", "立即支付", "确认支付", "确认付款", "购买", "立即购买", "订阅", "下单", "提交订单", "结算", "转账", // i18n-ignore: matched against apps' labels
		"支払", "購入", "注文を確定", "今すぐ購入", // i18n-ignore: matched against apps' labels
	},
	Deletion: {
		"delete", "remove", "erase", "clear all", "clear history", "clear data", "empty trash", "delete all", "delete account",
		"trash", "move to trash", "move to bin", "废纸篓", "移到废纸篓", "ゴミ箱", // i18n-ignore: matched against apps' labels
		"删除", "移除", "清空", "抹掉", "全部删除", "永久删除", "确认删除", "清除", // i18n-ignore: matched against apps' labels
		"削除", "消去", "すべて削除", // i18n-ignore: matched against apps' labels
	},
	SendMessage: {
		"send", "send message", "send now", "post",
		"发送", "发送消息", "发表", "发布", // i18n-ignore: matched against apps' labels
		"送信", "投稿", // i18n-ignore: matched against apps' labels
	},
	Password: {
		"password", "passcode", "passwort", "mot de passe", "pin", "verification code", "security code",
		"密码", "口令", "验证码", "支付密码", // i18n-ignore: matched against apps' labels
		"パスワード", "パスコード", "暗証番号", "確認コード", // i18n-ignore: matched against apps' labels
	},
}

// Messaging apps, where typing a line break sends what was typed. Payment apps, where any touch can pay.
var (
	builtinMessaging = []string{"com.apple.MobileSMS", "com.apple.mobilemail", "net.whatsapp.WhatsApp",
		"ph.telegra.Telegraph", "com.tencent.xin", "com.burbn.instagram", "com.facebook.Messenger",
		"com.hammerandchisel.discord", "com.tinyspeck.chatlyio", "com.atebits.Tweetie2", "com.bytedance.ee.lark"}
	builtinPayment = []string{"com.apple.Passbook"}
)

// price is a label that is an amount of money ($4.99, ¥12, 12.00 €), what store buttons say.
var price = lazyre.New(`^\s*(?:[$€£¥₩₹]\s?\d|\d+(?:[.,]\d{1,2})?\s?(?:€|£|元|円|usd|eur|cny|jpy)(?:[^a-z]|$))`)

// matches says whether label is one of phrases or starts with one.
func matches(label string, phrases []string) (string, bool) {
	l := strings.Join(strings.Fields(strings.ToLower(label)), " ")
	if l == "" {
		return "", false
	}
	for _, p := range phrases {
		if !strings.HasPrefix(l, p) {
			continue
		}
		rest := l[len(p):]
		if rest == "" || !latin(p) {
			return p, true
		}
		if r, _ := utf8.DecodeRuneInString(rest); r == ' ' || strings.ContainsRune(":,.!?…-–—(", r) {
			return p, true
		}
	}
	return "", false
}

// latin says whether a phrase is written in a script with spaces between words, where "pay" must not match "payee".
func latin(p string) bool {
	r, _ := utf8.DecodeRuneInString(p)
	return r < 0x2E80
}

func (r Rules) words(k Sensitivity) []string {
	return append(append([]string(nil), builtinWords[k]...), r.Words[k]...)
}

// self says whether a is Persona's own app: its bundle id is one of SelfApps or its name, whole, is one of SelfNames.
// Either is enough, so the rule holds with no bundle id known; another app of the same name is refused too.
func (r Rules) self(a App) bool {
	return a.BundleID != "" && listed(r.SelfApps, a.BundleID) || a.Name != "" && listed(r.SelfNames, a.Name)
}

// listed compares ids and names as people write them: case and surrounding spaces aside.
func listed(list []string, v string) bool {
	v = strings.TrimSpace(v)
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), v) })
}

func (r Rules) messaging(a App) bool {
	return slices.Contains(builtinMessaging, a.BundleID) || slices.Contains(r.MessagingApps, a.BundleID)
}

func (r Rules) payment(a App) bool {
	return slices.Contains(builtinPayment, a.BundleID) || slices.Contains(r.PaymentApps, a.BundleID)
}

// Touch is what a step's input lands on, on the read just before it.
type Touch struct {
	Target, To *Numbered // the elements the step names, or the innermost under its points
	// Under and UnderTo are every element containing where the input starts and ends: a tap on a button's text
	// label lands on the button too.
	Under, UnderTo []Numbered
	From, End      *Point // where a touch starts and ends
}

// swipeDeletes is how far left (points) a swipe on a row has to go to count as swipe-to-delete.
const swipeDeletes = 30.0

// Check is the rules' verdict on step with touch on screen (the read just before it; nil when there is none).
func (r Rules) Check(step Step, t Touch, screen *Screen) Verdict {
	var v Verdict
	var app App
	if screen != nil {
		app = screen.App
	}
	switch {
	case step.Action == ActLaunch && listed(r.SelfApps, step.App):
		v.Refused = "Persona's own app is where the owner approves steps: the automation never operates it"
		return v
	case touches(step.Action) && r.self(app):
		v.Refused = "Persona's own app is in front, where the owner approves steps: the automation never operates it; " +
			"launch another app or press home first"
		return v
	}
	if touches(step.Action) && r.payment(app) {
		v.need(Payment, "app "+app.String())
	}
	touched := t.elements()
	switch step.Action {
	case ActTap, ActLongPress, ActDrag, ActSwipe:
		for _, e := range touched {
			r.checkElement(&v, e.Element)
		}
		// Swiping a row to the left deletes it outright in many apps (Mail, Messages).
		if (step.Action == ActSwipe || step.Action == ActDrag) && t.From != nil && t.End != nil {
			dx, dy := t.End.X-t.From.X, t.End.Y-t.From.Y
			if dx <= -swipeDeletes && -dx > math.Abs(dy) {
				for _, e := range append(append([]Numbered(nil), t.Under...), deref(t.Target)...) {
					if e.Role == RoleCell {
						v.need(Deletion, "swipe left on "+describe(e.Element))
						break
					}
				}
			}
		}
	case ActType:
		r.checkTyping(&v, step, t, screen, app)
		// The tap that focuses the field lands on what is under it.
		for _, e := range t.Under {
			r.checkElement(&v, e.Element)
		}
	case ActKey:
		if step.Key == "return" && r.messaging(app) {
			v.need(SendMessage, "return pressed in "+app.String())
		}
	}
	return v
}

func deref(e *Numbered) []Numbered {
	if e == nil {
		return nil
	}
	return []Numbered{*e}
}

// elements are the touched elements, each once.
func (t Touch) elements() []Numbered {
	var out []Numbered
	seen := map[int]bool{}
	for _, list := range [][]Numbered{deref(t.Target), deref(t.To), t.Under, t.UnderTo} {
		for _, e := range list {
			if !seen[e.N] {
				seen[e.N] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// passive are roles that only show something: tapping one doesn't pay, delete or send, whatever it says (the
// button it sits on is checked on its own).
var passive = map[string]bool{"staticText": true, "heading": true, "image": true}

// names are what an element is called: its label and, written as words, the app's identifier for it
// (deleteButton → delete button).
func names(e Element) []string {
	out := []string{e.Label}
	if e.Identifier != "" {
		out = append(out, words(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(e.Identifier)))
	}
	return out
}

func (r Rules) matchesAny(texts []string, k Sensitivity) bool {
	for _, s := range texts {
		if _, ok := matches(s, r.words(k)); ok {
			return true
		}
	}
	return false
}

func (r Rules) checkElement(v *Verdict, e Element) {
	if passive[e.Role] {
		return
	}
	what, ns := describe(e), names(e)
	if r.matchesAny(ns, Payment) || (e.Role == RoleButton && price.MatchString(strings.ToLower(e.Label))) {
		v.need(Payment, what)
	}
	// The keyboard's own delete key erases a character, not a thing.
	if r.matchesAny(ns, Deletion) && e.Role != RoleKeyboardKey {
		v.need(Deletion, what)
	}
	if r.matchesAny(ns, SendMessage) {
		v.need(SendMessage, what)
	}
}

func (r Rules) checkTyping(v *Verdict, step Step, t Touch, screen *Screen, app App) {
	var field *Numbered
	switch {
	case t.Target != nil:
		field = t.Target
	case step.Target != nil:
		// The step taps a point with no element under it: which field that focuses is unknown.
	case screen != nil:
		if f, ok := screen.Focused(); ok {
			field = &f
		}
	}
	switch {
	case field != nil && field.secure():
		v.need(Password, "secure field "+describe(field.Element))
	case field != nil:
		// A placeholder is often the field's value until something is typed.
		if r.matchesAny(append(names(field.Element), field.Value), Password) {
			v.need(Password, "field "+describe(field.Element))
		}
	case screen != nil && hasSecureField(screen):
		// Nothing says where the keyboard is, and a password field is on screen: it may be the one.
		v.need(Password, "a secure field is on screen and none is known to have the keyboard")
	}
	if strings.ContainsAny(step.Text, "\n\r") && r.messaging(app) {
		v.need(SendMessage, "line break typed in "+app.String())
	}
}

func hasSecureField(s *Screen) bool {
	for _, e := range s.Elements {
		if e.secure() {
			return true
		}
	}
	return false
}

// ElementRef names the element a step acts on.
type ElementRef struct {
	N     int    `json:"n,omitempty"`
	Role  string `json:"role"`
	Label string `json:"label,omitempty"`
}

func ref(e *Numbered) *ElementRef {
	if e == nil {
		return nil
	}
	return &ElementRef{N: e.N, Role: e.Role, Label: e.Label}
}

// Approval is a step waiting for the owner: what an approval card needs to say what will happen, as data, for the
// manager to put in the owner's language.
type Approval struct {
	Step   int           `json:"step"` // 1-based, within the do the model asked for
	Action string        `json:"action"`
	Needs  []Sensitivity `json:"needs"`
	App    App           `json:"app"`
	Target *ElementRef   `json:"target,omitempty"`
	// Text is what a type step will type; left out for a password, of which only the length is given.
	Text      string   `json:"text,omitempty"`
	TextLen   int      `json:"text_len,omitempty"`
	Key       string   `json:"key,omitempty"`
	Button    string   `json:"button,omitempty"`
	LaunchApp string   `json:"launch_app,omitempty"` // launch, terminate
	Evidence  []string `json:"evidence,omitempty"`
	// Steps are this step and the ones after it, with Confirm: one answer covers them, though a step the rules mark
	// is still asked about on its own (and, without an element tree, every input is).
	Steps []StepSummary `json:"steps,omitempty"`
	// At is where a touch lands, when there are no elements to name it by (Unchecked).
	At *Point `json:"at,omitempty"`
}

// StepSummary is a step as data, for an approval card.
type StepSummary struct {
	Action  string      `json:"action"`
	Target  *ElementRef `json:"target,omitempty"`
	At      *Point      `json:"at,omitempty"` // a target given as a point
	TextLen int         `json:"text_len,omitempty"`
	Key     string      `json:"key,omitempty"`
	Button  string      `json:"button,omitempty"`
	App     string      `json:"app,omitempty"`
}

func newApproval(i int, step Step, target *Numbered, screen *Screen, v Verdict) Approval {
	a := Approval{Step: i + 1, Action: step.Action, Needs: v.Needs, Evidence: v.Evidence, Target: ref(target),
		Key: step.Key, Button: step.Button, LaunchApp: step.App}
	if screen != nil {
		a.App = screen.App
	}
	if step.Action == ActType {
		a.TextLen = utf8.RuneCountInString(step.Text)
		if !slices.Contains(v.Needs, Password) {
			a.Text = step.Text
		}
	}
	return a
}

// summary is st for an approval card. Its target is named as it is on scr, the screen the owner is asked on (nil when
// its numbers no longer hold): its number, role and label, for an element the step names by number or by a label only
// one element has; otherwise as the step wrote it.
func summary(st Step, scr *Screen) StepSummary {
	s := StepSummary{Action: st.Action, Key: st.Key, Button: st.Button, App: st.App}
	if st.Action == ActType {
		s.TextLen = utf8.RuneCountInString(st.Text)
	}
	t := st.Target
	if t == nil {
		return s
	}
	if p, ok := t.point(); ok {
		s.At = &p
		return s
	}
	s.Target = &ElementRef{N: t.N, Role: t.Role, Label: t.Label}
	if scr == nil {
		return s
	}
	switch {
	case t.byNumber():
		if e, ok := scr.Find(t.N); ok {
			s.Target = ref(&e)
		}
	case t.Label != "":
		if found := scr.byLabel(t.Label, t.Role); len(found) == 1 {
			s.Target = ref(&found[0])
		}
	}
	return s
}
