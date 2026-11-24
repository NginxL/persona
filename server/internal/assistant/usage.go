package assistant

import (
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Using information by its categories and the scene it goes to (#13246, docs/assistant/health.md "Using information
// by scene"). Collecting is not limited; what a piece of information may do is decided where it is used, by one
// function, usage, from what the information is (its categories, whether they are all known, the track it came from)
// and where it goes (one of seven scenes). It never reads a track's name or kind: a track takes part only through
// its default categories, which every piece of information from it carries.

// scene is where a piece of information is used.
type scene int

const (
	// sceneSourceTrack: inside the track it came from (its conversations, its scheduled tasks, its channel with only
	// the user and the bot).
	sceneSourceTrack scene = iota + 1
	// sceneElsewhere: in the main chat or another track.
	sceneElsewhere
	// sceneHandOff: handed to an agent or another computer (a dispatch's first message, agent_send, agent_start, a
	// task plan).
	sceneHandOff
	// sceneLockScreen: on the lock screen and in pushes (APNs, Persona Link, the phone's local notifications, briefing
	// cards).
	sceneLockScreen
	// sceneOthers: sent to other people (Lark, WeChat, mail, a comment, a connector's write, a channel's all-cards).
	sceneOthers
	// sceneExtension: read by a third-party extension (session lists, transcripts, cards, to-dos, the feed, memory, the
	// knowledge base).
	sceneExtension
	// sceneKBWrite: written to the shared knowledge base.
	sceneKBWrite
)

var scenes = []scene{sceneSourceTrack, sceneElsewhere, sceneHandOff, sceneLockScreen, sceneOthers, sceneExtension, sceneKBWrite}

// usageRule is what a piece of information may do in a scene.
type usageRule int

const (
	// usageAllow: used as it is.
	usageAllow usageRule = iota + 1
	// usageRelevant: used where it is relevant (recall, prefetch by the task's or the question's words).
	usageRelevant
	// usageMinimal: used only when the conversation, the question or the task carries its categories, and only what
	// is needed; never in a standing context.
	usageMinimal
	// usageRedact: only that there is news, without its words, title, conversation, track or task.
	usageRedact
	// usageConfirm: confirmed on the phone each time, with no "always allow".
	usageConfirm
	// usageDeny: not used.
	usageDeny
)

// infoUse is what usage knows of a piece of information.
type infoUse struct {
	// Categories are the information categories it carries (normalCategories); none is general.
	Categories []string
	// Known: its categories were all told. Information not tagged yet, or whose tagging failed or timed out, is not
	// known: it is used as usual in its own track and taken for sensitive everywhere else.
	Known bool
	// SourceTrack is the track it came from ("" the main track).
	SourceTrack string
	// Rules are its source track's usage rules (TrackKindConfig.Usage, #13472): each makes one scene stricter for
	// information of one category ("*" all of it), never looser (tightensUsage checked them when they were saved).
	Rules []assistantwire.TrackUsageRule
}

// sensitive: info carries a category other than general, or its categories are not known.
func (info infoUse) sensitive() bool { return !info.Known || len(info.Categories) > 0 }

// extensionGrant is what an extension may read (sceneExtension): an extension the user owns (trust owner, such as
// their memory extension) reads everything; a third-party one reads the sensitive categories the user granted it.
type extensionGrant struct {
	Owner      bool
	Categories []string
}

// covers: g lets an extension read information of cats, every one of which it was granted (all known).
func (g extensionGrant) covers(info infoUse) bool {
	if g.Owner {
		return true
	}
	if !info.Known {
		return false
	}
	for _, c := range info.Categories {
		if !slices.Contains(g.Categories, c) {
			return false
		}
	}
	return true
}

// usage is what info may do in scene sc (docs/assistant/health.md "Using information by scene"); grant is the reading
// extension's, for sceneExtension only. General information is used as usual everywhere, by relevance outside its
// track; sensitive information (or information whose categories are not known) is used freely in its own track only:
// elsewhere only when on topic, handed out or sent to others only once the phone confirmed it, on the lock screen
// only as news, read by an extension only with a grant for each of its categories, and never written to the shared
// knowledge base. What the source track's kind and the track itself tighten (info.Rules) comes on top; it can only
// make a rule stricter.
func usage(info infoUse, sc scene, grant extensionGrant) usageRule {
	return tightened(info, sc, baseUsage(info, sc, grant))
}

// tightened is rule, made stricter by every one of info's track rules for scene sc that covers it: a rule for "*"
// covers all of its information, one for a category covers it when it carries that category or its categories are
// not known (it may carry any). A rule never loosens: the stricter of the two wins.
func tightened(info infoUse, sc scene, rule usageRule) usageRule {
	for _, r := range info.Rules {
		rsc, ok := trackUsageScenes[r.Scene]
		if !ok || rsc != sc {
			continue
		}
		if r.Category != trackKindAll && info.Known && !slices.Contains(info.Categories, r.Category) {
			continue
		}
		if more, ok := trackUsageRules[r.Rule]; ok && more > rule {
			rule = more
		}
	}
	return rule
}

// baseUsage is usage before the track's rules: the assistant's own rule for info in scene sc.
func baseUsage(info infoUse, sc scene, grant extensionGrant) usageRule {
	sensitive := info.sensitive()
	switch sc {
	case sceneSourceTrack:
		return usageAllow
	case sceneElsewhere:
		if sensitive {
			return usageMinimal
		}
		return usageRelevant
	case sceneHandOff:
		if sensitive {
			return usageConfirm
		}
		return usageRelevant
	case sceneLockScreen:
		if sensitive {
			return usageRedact
		}
		return usageAllow
	case sceneOthers:
		if sensitive {
			return usageConfirm
		}
		return usageAllow
	case sceneExtension:
		if sensitive && !grant.covers(info) {
			return usageDeny
		}
		return usageAllow
	case sceneKBWrite:
		if sensitive {
			return usageDeny
		}
		return usageAllow
	}
	return usageDeny // a scene this manager does not know: the strict side
}

// policyOf sums up usage for information info as the switches the call sites read (TrackPolicy), each the rule of one
// scene: the lock screen redacts, a third-party extension without grants is denied, the knowledge base is denied, a
// hand-off is confirmed, and outside its track it is used only when on topic (which keeps the main chat's file tools
// out of its track's directory). A switch is on for its rule or any stricter one, so a track rule that tightens a scene
// past it (deny on the lock screen, say) never turns it off.
func policyOf(info infoUse) TrackPolicy {
	return TrackPolicy{
		Redact:             usage(info, sceneLockScreen, extensionGrant{}) >= usageRedact,
		HideFromExtensions: usage(info, sceneExtension, extensionGrant{}) == usageDeny,
		NoKBWrite:          usage(info, sceneKBWrite, extensionGrant{}) == usageDeny,
		ConfirmOutbound:    usage(info, sceneHandOff, extensionGrant{}) >= usageConfirm,
		DenyMainRead:       usage(info, sceneElsewhere, extensionGrant{}) >= usageMinimal,
	}
}
