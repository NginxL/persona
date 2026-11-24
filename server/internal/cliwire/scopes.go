package cliwire

// ReadAsMeScopes is the least-privilege user grant used by the built-in
// connector. Keep each entry next to the command that needs it so a new read
// path cannot silently expand the sign-in request. lark-cli adds
// offline_access itself when it creates a refreshable user grant.
var ReadAsMeScopes = []string{
	"im:chat:read",                     // im +chat-list (including p2p chats)
	"im:message:readonly",              // im +chat-messages-list, +threads-messages-list, +messages-search content reads
	"im:message.group_msg:get_as_user", // the same message commands for group chats as the user
	"im:message.p2p_msg:get_as_user",   // the same message commands for p2p chats as the user
	"im:message.reactions:read",        // default reaction enrichment on the message commands
	"search:message",                   // im +messages-search (Inbox polling and larkcli_search messages)
	"search:docs:read",                 // drive +search (larkcli_search docs)
	"contact:user:search",              // contact +search-user (larkcli_search people)
	"docs:document.content:read",       // docs +fetch (watched-document inspection)
	"docs:document.comment:read",       // drive +list-comments / +list-replies (comment Inbox)
	"wiki:space:retrieve",              // wiki +space-list (comment Inbox document discovery)
	"wiki:node:retrieve",               // wiki +node-list (comment Inbox document discovery)
}
