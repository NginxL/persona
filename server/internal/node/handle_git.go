package node

// The git.* methods (Node.handle, #8089); see handle_file.go.
const (
	methodGitBlob     = "git.blob"
	methodGitStatus   = "git.status"
	methodGitPull     = "git.pull"
	methodGitDiff     = "git.diff"
	methodGitShow     = "git.show"
	methodGitReview   = "git.review"
	methodGitLog      = "git.log"
	methodGitStage    = "git.stage"
	methodGitCommit   = "git.commit"
	methodGitPush     = "git.push"
	methodGitBranches = "git.branches"
	methodGitSwitch   = "git.switch"
	methodGitStash    = "git.stash"
	methodGitPR       = "git.pr"
	methodGitPRCreate = "git.pr_create"
	methodGitPRMerge  = "git.pr_merge"
)

// methodGitBackendPrefix starts a git.* method addressed to a repository of a backend task workspace (#11687):
// git.backend.<verb> runs git.<verb> in the repository its workspace and repo ids name (task_workspace_git_route.go).
const methodGitBackendPrefix = "git.backend."

// gitStatusParams is git.status's request; IdentityOnly asks for the repository's author identity alone.
type gitStatusParams struct {
	Cwd          string `json:"cwd"`
	IdentityOnly bool   `json:"identity_only"`
}

// gitCwdParams is git.pull's request: the repository's folder.
type gitCwdParams struct {
	Cwd string `json:"cwd"`
}

// gitDiffParams is git.diff's request.
type gitDiffParams struct {
	Cwd    string `json:"cwd"`
	Path   string `json:"path"`
	Staged bool   `json:"staged"`
}

// gitShowParams is git.show's request: one commit.
type gitShowParams struct {
	Cwd  string `json:"cwd"`
	Hash string `json:"hash"`
}

// gitLogParams is git.log's request.
type gitLogParams struct {
	Cwd   string `json:"cwd"`
	Skip  int    `json:"skip"`
	Limit int    `json:"limit"`
}
