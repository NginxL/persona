package proto

// Files sent from the phone (POST /api/v1/machines/{mid}/files): the hub streams the body to the node in chunks.
const (
	MaxUploadBytes   = 200 << 20
	UploadChunkBytes = 1 << 20
	// A file pasted or dropped into a web terminal (dest=terminal) lands under the node's Persona directory and is
	// smaller: what an agent reads by path is a screenshot or a document, not a video.
	UploadDestTerminal     = "terminal"
	MaxTerminalUploadBytes = 50 << 20
	// A checkup report or lab result for the health space (dest=health, docs/assistant/health.md "Checkup reports"): it lands in the
	// health directory's reports/ and stays, where the health conversations read it and the others are denied.
	UploadDestHealth = "health"
	// A file a task's Agent made and handed to its conversation (dest=file, docs/assistant/agent-work.md "Files a task
	// hands over"): the manager copies it from the Agent's computer to the conversation's computer, under the node's
	// uploads/files/<date>/. It is kept like the chat's images, not swept like a terminal paste, so a message that
	// names it still opens later.
	UploadDestFile     = "file"
	MaxFileUploadBytes = MaxTerminalUploadBytes
	// A file put into a folder the app is browsing (dest=dir&dir=…, docs/protocol/protocol.md "Changing files"): it keeps its own
	// name, only folders in the home take it (node/writeguard.go), and it may be bigger.
	UploadDestDir     = "dir"
	MaxDirUploadBytes = 2 << 30
	// A file sent to a computer to keep (dest=inbox, docs/protocol/file-transfer.md): it lands in the machine's receive
	// folder (hub.json machines[].inbox_dir, DefaultInboxDir by default) under its own name, a taken name gets " 2",
	// " 3"… The limit is a folder's: the bytes are streamed a chunk at a time, so nothing holds the whole file, and
	// what a person sends between their own computers (a video, a disk image) is often over 200 MB.
	UploadDestInbox     = "inbox"
	MaxInboxUploadBytes = MaxDirUploadBytes
	DefaultInboxDir     = "~/Downloads/Persona"
	// A file sent with a chat message and kept for good (dest=attachment, docs/protocol/protocol.md "Sending files",
	// #8361): it lands beside the pictures the chat sent that day, in the node's uploads/<date>/, under its own name
	// (a taken one gets " 2"), where agents read it and nothing sweeps it. The limit is MaxUploadBytes. A node from
	// before it uses its temp directory instead (the hub falls back), as every chat attachment did before.
	UploadDestAttachment = "attachment"
	// What dest=dir does when the name is taken: "" refuses with CodeExists (409, the app asks), replace overwrites
	// the file, keep puts it beside as "name 2.ext".
	UploadConflictReplace = "replace"
	UploadConflictKeep    = "keep"
	// CodeReadOnly: the path is outside what the phone may change (409); CodeExists: the name is taken (409, name).
	CodeReadOnly = "read_only"
	CodeExists   = "exists"
	// CodeNotFound: the file to rename, move, trash or put back is not there (404).
	CodeNotFound = "not_found"
	// CodeTooLarge: over MaxUploadBytes (413); CodeNodeOutdated: the machine's node predates the feature (409).
	CodeTooLarge     = "too_large"
	CodeNodeOutdated = "node_outdated"
)

// UploadedFile is where an upload landed on the machine.
type UploadedFile struct {
	Path string `json:"path"` // absolute
	Name string `json:"name"`
	Size int64  `json:"size"`
}
