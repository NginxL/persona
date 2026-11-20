package kb

import (
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The language of what the knowledge base writes from a recording for the user to read (docs/assistant/
// meeting-minutes.md "How the hub generates them"): a meeting's minutes, a recording's summary and a day's recap,
// their Markdown's headings included. It is the recording's language, whatever languages its transcript mixes;
// minutes.json and recap.json keep it, so a later render of their Markdown (a photo deleted, an erase) says the same.

// outputLang is what recording r's minutes, summary or recap are written in: the recording's language (the one the
// phone transcribed it in: zh-CN, en-US, ja-JP), else the owner's (OwnerLanguage), else zh-Hans. A recording's language
// the hub has no catalog for (ko-KR) counts as none: its headings could not be said in it.
func (e *Engine) outputLang(r *Recording) i18n.Lang {
	if l := i18n.Match(r.Language); l != "" {
		return l
	}
	if e.OwnerLanguage != nil {
		if l := e.OwnerLanguage(); i18n.Valid(l) {
			return l
		}
	}
	return i18n.ZhHans
}

// writingOwner is the system prompt's data for text written in lang (prompts.KBOwner.Language: "" for Chinese, the
// prompts' own, else the language's own name).
func (e *Engine) writingOwner(lang i18n.Lang) prompts.KBOwner {
	o := prompts.KBOwner{Owner: e.ownerName()}
	if lang != i18n.ZhHans {
		o.Language = lang.Name()
	}
	return o
}

// knowledgeOwner selects the owner's language once for a new non-recording model call.
// It does not schedule refreshes or change stored source content.
func (e *Engine) knowledgeOwner() prompts.KBOwner {
	lang := i18n.ZhHans
	if e.OwnerLanguage != nil {
		if l := e.OwnerLanguage(); i18n.Valid(l) {
			lang = l
		}
	}
	return e.writingOwner(lang)
}

// storedLang is the language of minutes or a recap that say lang: one written before they said any is Chinese.
func storedLang(lang string) i18n.Lang { return i18n.Lang(lang).Or(i18n.ZhHans) }
