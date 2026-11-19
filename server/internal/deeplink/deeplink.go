// Package deeplink builds the links the hub writes to Persona's pages: the paths of the route table in
// docs/protocol/deep-links.md (#9926), and their two forms. The web link is <public_url><path>, what the hub sends out
// (Lark cards take only http(s), a browser opens it where Persona is not installed); every app opens its own hub's web
// links natively. The app link is persona://open<path>, for the apps themselves.
//
// Path segments are escaped as url.PathEscape does and query values as url.QueryEscape; the query keys follow the
// table's order. spec/fixtures/deep-links/cases.json holds what each builder writes, byte for byte.
package deeplink

import (
	"net/url"
	"strings"
)

// AppPrefix is what an app link has in place of the hub's address.
const AppPrefix = "persona://open"

// Web is path's link on the hub's web app at base (its public_url; a trailing "/" is dropped, a sub-path kept); ""
// when base is, so a hub with no public address writes no link.
func Web(base, path string) string {
	base = strings.TrimRight(base, "/")
	if base == "" || path == "" {
		return ""
	}
	return base + path
}

// App is path's app link (persona://open<path>).
func App(path string) string {
	if path == "" {
		return ""
	}
	return AppPrefix + path
}

// Persona is the assistant's main page.
func Persona() string { return "/persona" }

// NewConversation is a new conversation with the assistant.
func NewConversation() string { return "/persona/new" }

// Conversation is one of the assistant's conversations.
func Conversation(id string) string { return "/persona/" + seg(id) }

// ConversationOf is a conversation of the assistant on machine run by manager (several assistants), either "" when not
// said.
func ConversationOf(id, machine, manager string) string {
	return query(Conversation(id), "machine", machine, "manager", manager)
}

// Session is an Agent's conversation.
func Session(machine, session string) string { return "/s/" + seg(machine) + "/" + seg(session) }

// At is path at one message (item, the message's id): the page lands on it. path unchanged when item is "".
func At(path, item string) string { return query(path, "at", item) }

// Agents is the list of Agents.
func Agents() string { return "/agents" }

// Inbox is the Inbox.
func Inbox() string { return "/inbox" }

// InboxItem is one Inbox item; manager the assistant whose item it is, "" for the current one.
func InboxItem(id, manager string) string { return query("/inbox/"+seg(id), "manager", manager) }

// Todos is the to-do list.
func Todos() string { return "/todos" }

// Todo is one to-do. It is a path: the older /todos?id=<id> is still read by every client, never written.
func Todo(id string) string { return "/todos/" + seg(id) }

// Knowledge is the knowledge base, or one kind of its entries when kind is not "".
func Knowledge(kind string) string {
	if kind == "" {
		return "/kb"
	}
	return "/kb/" + seg(kind)
}

// KnowledgeEntry is one entry of the knowledge base: kind is projects, topics, chats, people, docs, meetings, events or
// recordings; an id holding "/" stays one segment.
func KnowledgeEntry(kind, id string) string { return "/kb/" + seg(kind) + "/" + seg(id) }

// Recording is one recording's page in the knowledge base.
func Recording(id string) string { return KnowledgeEntry("recordings", id) }

// Meeting is one meeting's page in the knowledge base.
func Meeting(id string) string { return KnowledgeEntry("meetings", id) }

// Machines is the list of computers.
func Machines() string { return "/machines" }

// Machine is one computer.
func Machine(id string) string { return "/machines/" + seg(id) }

// settingsItems is the query key that names an item of a settings tab, for the tabs that have one
// (web/src/views/settings/nav.ts).
var settingsItems = map[string]string{"models": "purpose", "channels": "channel", "extensions": "extension", "connectors": "connector"}

// Settings is Settings, at tab when it is not "", at that tab's item when the tab has one and item is not "".
func Settings(tab, item string) string {
	if tab == "" {
		return "/settings"
	}
	key := settingsItems[tab]
	if key == "" {
		item = ""
	}
	return query("/settings", "tab", tab, key, item)
}

// ExtensionSettings is an extension's page in Settings.
func ExtensionSettings(id string) string { return Settings("extensions", id) }

// Extension is a page an extension draws (its ui), in the state it is given ("" for its first).
func Extension(extension, ui, state string) string {
	return query("/extension/"+seg(extension)+"/"+seg(ui), "state", state)
}

// seg is one path segment.
func seg(s string) string { return url.PathEscape(s) }

// query adds key/value pairs to path in their order, leaving out empty values, after any query path already has.
func query(path string, kv ...string) string {
	var b strings.Builder
	b.WriteString(path)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i] == "" || kv[i+1] == "" {
			continue
		}
		b.WriteString(sep)
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(kv[i+1]))
		sep = "&"
	}
	return b.String()
}
