# Persona conversations

For “Tag the release chat as launch,” identify the chat with `conversations_list` in the current space. If several match, resolve which one the person means before changing any. Read its existing tags from the list. `conversation_tags_set` replaces the entire set, so add the requested tag to the current set, or remove only the named tag. An explicit request to clear all tags uses an empty list. Report the returned tags.

Read recent exchanges with `conversation_read`. Rename an ordinary chat with `conversation_rename` using the owner's requested title or the tool's naming rule. Main has no title; renaming a direction is an action for main. Archive only on request. To restore a chat, include archived conversations in the list, then use `conversation_unarchive` on the matching ID.

Open a thread with `conversation_new` when the owner wants a separate conversation. Use `conversation_send` to deliver an actual message to an existing one; a queued receipt means it is already scheduled for delivery. Follow the conversation policy if a write is refused.
