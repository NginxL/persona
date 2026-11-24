package node

// The fs.* methods (Node.handle, #8089); see handle_file.go.
const (
	methodFsStat     = "fs.stat"
	methodFsList     = "fs.list"
	methodFsTaskList = "fs.task.list"
	methodFsFind     = "fs.find"
	methodFsWatch    = "fs.watch"
	methodFsChanged  = "fs.changed" // the node's notification to the hub when a watched folder changes
)

// fsStatParams is fs.stat's request (files_info.go); Detail asks for the Info sheet's fields.
type fsStatParams struct {
	Paths  []string `json:"paths"`
	Cwd    string   `json:"cwd"`
	Detail bool     `json:"detail"`
}

// fsListParams is fs.list's request (files.go listDir).
type fsListParams struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd"`
}

// fsFindParams is fs.find's request (files_find.go).
type fsFindParams struct {
	Cwd   string `json:"cwd"`
	Limit int    `json:"limit"`
}
