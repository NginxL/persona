package node

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Node.handle's methods decode their request into a struct of their own (#8089). Each case decodes one request sample
// two ways: into the struct the method has now, and into the fields as the shared parameter struct declared them before
// (the tags copied here, so a changed tag fails). The two must give the same values. The samples carry fields of other
// methods, which both ignore.
func TestTypedHandleParamsDecodeAsTheSharedStructDid(t *testing.T) {
	cases := []struct {
		name   string
		sample string
		legacy any // the old fields, with the old tags
		typed  any // the method's struct now
		same   func(legacy, typed any) bool
	}{
		{
			name:   methodFileRead,
			sample: `{"path":"a/b.txt","cwd":"/work","offset":4096,"stream":true,"session_id":"s1","tab_id":"t","limit":5}`,
			legacy: &struct {
				Path   string `json:"path"`
				Cwd    string `json:"cwd"`
				Offset int64  `json:"offset"`
				Stream bool   `json:"stream"`
			}{},
			typed: &fileReadParams{},
			same: func(l, n any) bool {
				return reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(fileReadParams{})).Interface() == *n.(*fileReadParams)
			},
		},
		{
			name:   methodFsStat,
			sample: `{"paths":["a","b"],"cwd":"/work","detail":true,"path":"x","session_id":"s1"}`,
			legacy: &struct {
				Paths  []string `json:"paths"`
				Cwd    string   `json:"cwd"`
				Detail bool     `json:"detail"`
			}{},
			typed: &fsStatParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(fsStatParams{})).Interface(), *n.(*fsStatParams))
			},
		},
		{
			name:   methodFsList,
			sample: `{"path":"sub","cwd":"/work","limit":3,"detail":true}`,
			legacy: &struct {
				Path string `json:"path"`
				Cwd  string `json:"cwd"`
			}{},
			typed: &fsListParams{},
			same: func(l, n any) bool {
				return reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(fsListParams{})).Interface() == *n.(*fsListParams)
			},
		},
		{
			name:   methodFsFind,
			sample: `{"cwd":"/work","limit":200,"path":"x"}`,
			legacy: &struct {
				Cwd   string `json:"cwd"`
				Limit int    `json:"limit"`
			}{},
			typed: &fsFindParams{},
			same: func(l, n any) bool {
				return reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(fsFindParams{})).Interface() == *n.(*fsFindParams)
			},
		},
		{
			name:   methodGitStatus,
			sample: `{"cwd":"/repo","identity_only":true,"path":"x"}`,
			legacy: &struct {
				Cwd          string `json:"cwd"`
				IdentityOnly bool   `json:"identity_only"`
			}{},
			typed: &gitStatusParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(gitStatusParams{})).Interface(), *n.(*gitStatusParams))
			},
		},
		{
			name:   methodGitPull,
			sample: `{"cwd":"/repo","hash":"abc","session_id":"s"}`,
			legacy: &struct {
				Cwd string `json:"cwd"`
			}{},
			typed: &gitCwdParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(gitCwdParams{})).Interface(), *n.(*gitCwdParams))
			},
		},
		{
			name:   methodGitDiff,
			sample: `{"cwd":"/repo","path":"a.go","staged":true,"hash":"abc"}`,
			legacy: &struct {
				Cwd    string `json:"cwd"`
				Path   string `json:"path"`
				Staged bool   `json:"staged"`
			}{},
			typed: &gitDiffParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(gitDiffParams{})).Interface(), *n.(*gitDiffParams))
			},
		},
		{
			name:   methodGitShow,
			sample: `{"cwd":"/repo","hash":"abc123","staged":true}`,
			legacy: &struct {
				Cwd  string `json:"cwd"`
				Hash string `json:"hash"`
			}{},
			typed: &gitShowParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(gitShowParams{})).Interface(), *n.(*gitShowParams))
			},
		},
		{
			name:   methodGitLog,
			sample: `{"cwd":"/repo","skip":50,"limit":25,"hash":"abc"}`,
			legacy: &struct {
				Cwd   string `json:"cwd"`
				Skip  int    `json:"skip"`
				Limit int    `json:"limit"`
			}{},
			typed: &gitLogParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(gitLogParams{})).Interface(), *n.(*gitLogParams))
			},
		},
		{
			name:   methodWorktreePrepare,
			sample: `{"composer":true,"kind":"herdr","workspace_id":"w1","cwd":"/repo","branch":"b"}`,
			// Branch inspection extends the shared request shape after the typed split.
			legacy: &struct {
				Composer    bool   `json:"composer"`
				Kind        string `json:"kind"`
				WorkspaceID string `json:"workspace_id"`
				Cwd         string `json:"cwd"`
				Branch      string `json:"branch"`
			}{},
			typed: &worktreePrepareParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreePrepareParams{})).Interface(), *n.(*worktreePrepareParams))
			},
		},
		{
			name:   methodWorktreeCreate,
			sample: `{"composer":false,"kind":"herdr","workspace_id":"w1","cwd":"/repo","branch":"feat","path":"/wt","parent_workspace_id":"w0","auto_name":true,"for_agent":true,"open_workspace":false,"locale":"en"}`,
			legacy: &struct {
				Composer          bool   `json:"composer"`
				Kind              string `json:"kind"`
				WorkspaceID       string `json:"workspace_id"`
				Cwd               string `json:"cwd"`
				Branch            string `json:"branch"`
				Path              string `json:"path"`
				ParentWorkspaceID string `json:"parent_workspace_id"`
				AutoName          bool   `json:"auto_name"`
				ForAgent          bool   `json:"for_agent"` // added after the split (#10624)
				OpenWorkspace     *bool  `json:"open_workspace"`
			}{},
			typed: &worktreeCreateParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeCreateParams{})).Interface(), *n.(*worktreeCreateParams))
			},
		},
		{
			name:   methodWorktreeSource,
			sample: `{"kind":"orca","workspace_id":"w1","cwd":"/repo","name":"x"}`,
			legacy: &struct {
				Kind        string `json:"kind"`
				WorkspaceID string `json:"workspace_id"`
			}{},
			typed: &worktreeSourceParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeSourceParams{})).Interface(), *n.(*worktreeSourceParams))
			},
		},
		{
			name:   methodWorktreeAdd,
			sample: `{"kind":"herdr","cwd":"/repo","branch":"b","name_hint":"fix","path":"/x"}`,
			legacy: &struct {
				Kind     string `json:"kind"`
				Cwd      string `json:"cwd"`
				Branch   string `json:"branch"`
				NameHint string `json:"name_hint"`
			}{},
			typed: &worktreeAddParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeAddParams{})).Interface(), *n.(*worktreeAddParams))
			},
		},
		{
			name:   methodWorktreeStatus,
			sample: `{"kind":"herdr","cwd":"/repo","branch":"b"}`,
			legacy: &struct {
				Kind string `json:"kind"`
				Cwd  string `json:"cwd"`
			}{},
			typed: &worktreeStatusParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeStatusParams{})).Interface(), *n.(*worktreeStatusParams))
			},
		},
		{
			name:   methodWorktreeRemove,
			sample: `{"kind":"herdr","path":"/wt","delete_branch":true,"force":true,"cwd":"/repo"}`,
			legacy: &struct {
				Kind         string `json:"kind"`
				Path         string `json:"path"`
				DeleteBranch bool   `json:"delete_branch"`
				Force        bool   `json:"force"`
			}{},
			typed: &worktreeRemoveParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeRemoveParams{})).Interface(), *n.(*worktreeRemoveParams))
			},
		},
		{
			name:   methodWorktreeSnapshot,
			sample: `{"kind":"herdr","cwd":"/repo","name_hint":"review","branch":"b"}`,
			legacy: &struct {
				Kind     string `json:"kind"`
				Cwd      string `json:"cwd"`
				NameHint string `json:"name_hint"`
			}{},
			typed: &worktreeSnapshotParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeSnapshotParams{})).Interface(), *n.(*worktreeSnapshotParams))
			},
		},
		{
			name:   methodWorktreeRename,
			sample: `{"kind":"herdr","workspace_id":"w1","name":"New","cwd":"/repo"}`,
			legacy: &struct {
				Kind        string `json:"kind"`
				WorkspaceID string `json:"workspace_id"`
				Name        string `json:"name"`
			}{},
			typed: &worktreeRenameParams{},
			same: func(l, n any) bool {
				return reflect.DeepEqual(reflect.ValueOf(l).Elem().Convert(reflect.TypeOf(worktreeRenameParams{})).Interface(), *n.(*worktreeRenameParams))
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(c.sample), c.legacy); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.sample), c.typed); err != nil {
				t.Fatal(err)
			}
			if !c.same(c.legacy, c.typed) {
				t.Fatalf("legacy %+v, typed %+v", c.legacy, c.typed)
			}
			if reflect.ValueOf(c.typed).Elem().IsZero() {
				t.Fatalf("sample decoded to nothing: %s", c.sample)
			}
		})
	}
}
