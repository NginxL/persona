package kbwire

import "context"

// Source is where chats come from (Feishu through the Lark extension).
type Source interface {
	Name() string // "lark"
	Chats(ctx context.Context, limit int) ([]SourceChat, error)
	// Messages returns messages newer than after (the latest page when after is ""), oldest first,
	// with the cursor to continue from.
	Messages(ctx context.Context, chat SourceChat, after string, limit int) (msgs []SourceMessage, newer string, more bool, err error)
	// Download saves one media path to dst.
	Download(ctx context.Context, media, dst string) error
}

// ItemSource lists documents, meetings and calendar events (Feishu through the Lark extension).
type ItemSource interface {
	Items(ctx context.Context) ([]Item, error)
	// DocTitle looks up a document's title by its link ("" when it cannot).
	DocTitle(ctx context.Context, url string) string
}
