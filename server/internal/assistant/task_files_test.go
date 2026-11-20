package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskFilePaths(t *testing.T) {
	reply := "做完了。\nfile: /tmp/report.md\n文件：`~/manual.pdf`\nfile: relative.txt\nfile: /tmp/report.md\nfile: \"/tmp/chart.png\"\n" +
		"<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\nartifact: file /tmp/extra.zip\n</closeout>"
	got := taskFilePaths(reply)
	want := []string{"/tmp/report.md", "~/manual.pdf", "/tmp/chart.png", "/tmp/extra.zip"}
	if len(got) != len(want) {
		t.Fatalf("paths: %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths: %q, want %q", got, want)
		}
	}
}

func TestTaskFilePathsLanguageMarkers(t *testing.T) {
	got := taskFilePaths("文件:/tmp/zh-ascii.md\nfile：「/tmp/fullwidth.md」\n")
	want := []string{"/tmp/zh-ascii.md", "/tmp/fullwidth.md"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("paths: %q, want %q", got, want)
	}
}

func TestTaskFilePathsMost(t *testing.T) {
	var b strings.Builder
	for i := 0; i < taskFilesMost+3; i++ {
		b.WriteString("file: /tmp/f" + string(rune('a'+i)) + "\n")
	}
	if got := taskFilePaths(b.String()); len(got) != taskFilesMost {
		t.Fatalf("paths: %d, want %d", len(got), taskFilesMost)
	}
}

func TestTaskSuccessorFilesLanguage(t *testing.T) {
	for _, tc := range []struct {
		locale string
		want   []string
	}{
		{locale: "zh-Hans", want: []string{"file: <这台电脑上的绝对路径>", "最多 5 个", "Persona 会把它们送到对话那边"}},
		{locale: "en", want: []string{"file: <absolute path on this computer>", "up to 5", "Persona will deliver them to the conversation"}},
		{locale: "ja", want: []string{"file: <このコンピューター上の絶対パス>", "最大 5 ファイル", "Persona が会話へ配送するため"}},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.sessions["box"] = nil
			f.hello.Locale = tc.locale
			s := newStorelessService(t, f)
			todo := Todo{ID: "todo_1", Title: "Hand over a report"}
			putTodo(s, todo)
			last := proto.Session{MachineID: "box", SessionID: "old", Cwd: "/code", Launcher: "claude", Agent: "claude"}
			if _, err := s.startTaskSuccessor(context.Background(), todo, last, "Continue"); err != nil {
				t.Fatalf("startTaskSuccessor: %v", err)
			}
			starts := startsOf(f)
			if len(starts) != 1 {
				t.Fatalf("got %d starts, want 1", len(starts))
			}
			prompt, _ := starts[0]["prompt"].(string)
			for _, want := range tc.want {
				if !strings.Contains(prompt, want) {
					t.Errorf("successor prompt is missing %q:\n%s", want, prompt)
				}
			}
		})
	}
}

// taskFileFake is what the two nodes answer: files on "box" (the Agent's computer), uploads on "server" (the
// conversation's).
type taskFileFake struct {
	mu      sync.Mutex
	on      map[string][]byte
	uploads []map[string]any
	upload  func(dest string) (string, error)
}

func newTaskFileFake(f *fakeHub, on map[string][]byte) *taskFileFake {
	ff := &taskFileFake{on: on}
	f.answerNode("file.chunk", func(machine string, raw json.RawMessage) (any, error) {
		var req proto.FileChunkRequest
		_ = json.Unmarshal(raw, &req)
		data, ok := ff.on[req.Path]
		if machine != "box" || !ok {
			return nil, errors.New("no such file")
		}
		out := proto.FileChunk{Path: req.Path, Name: req.Path[strings.LastIndex(req.Path, "/")+1:], Size: int64(len(data)), Revision: "r1"}
		if req.Length > 0 {
			out.Data = data[req.Offset:min(int64(len(data)), req.Offset+int64(req.Length))]
		}
		return out, nil
	})
	f.answerNode("file.upload.begin", func(machine string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		p["machine"] = machine
		ff.mu.Lock()
		defer ff.mu.Unlock()
		ff.uploads = append(ff.uploads, p)
		if ff.upload != nil {
			if _, err := ff.upload(p["dest"].(string)); err != nil {
				return nil, err
			}
		}
		return map[string]any{"upload_id": "u" + p["name"].(string), "dest": p["dest"]}, nil
	})
	f.answerNode("file.upload.chunk", func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerNode("file.upload.end", func(string, json.RawMessage) (any, error) {
		return proto.UploadedFile{Path: "/home/me/uploads/files/2026-11-18/ab12-file"}, nil
	})
	return ff
}

func TestDeliverTaskFilesCopies(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["box"] = []proto.Session{{SessionID: "s1", MachineID: "box"}}
	ff := newTaskFileFake(f, map[string][]byte{"/home/me/manual.md": []byte("# manual\n"), "/home/me/plot.png": {0x89, 'P', 'N', 'G'}})
	f.answerNode("file.upload.end", func(machine string, raw json.RawMessage) (any, error) {
		var p struct {
			UploadID string `json:"upload_id"`
		}
		_ = json.Unmarshal(raw, &p)
		return proto.UploadedFile{Path: "/home/me/.persona/uploads/files/2026-11-18/ab12-" + strings.TrimPrefix(p.UploadID, "u")}, nil
	})
	s := newStorelessService(t, f)
	todo := Todo{ID: "todo_1", Title: "取回手册", Origin: "conv1", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}}
	putTodo(s, todo)

	files := s.deliverTaskFiles(context.Background(), todo, "box", "server", "手册在这里：\nfile: /home/me/manual.md\nfile: /home/me/plot.png\n")
	if len(files) != 2 {
		t.Fatalf("files: %+v", files)
	}
	for _, x := range files {
		if x.Local == "" || x.Error != "" || !strings.HasPrefix(x.Local, "/home/me/.persona/uploads/files/") {
			t.Fatalf("file: %+v", x)
		}
	}
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if len(ff.uploads) != 2 {
		t.Fatalf("uploads: %+v", ff.uploads)
	}
	for _, u := range ff.uploads {
		if u["dest"] != proto.UploadDestFile || u["machine"] != "server" {
			t.Fatalf("upload: %+v", u)
		}
	}
	// A second turn names the same files: nothing is copied again.
	if again := s.deliverTaskFiles(context.Background(), todo, "box", "server", "file: /home/me/manual.md"); len(again) != 2 {
		t.Fatalf("files after a second turn: %+v", again)
	}
	if len(ff.uploads) != 2 {
		t.Fatalf("copied again: %+v", ff.uploads)
	}
}

func TestDeliverTaskFilesSameComputer(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newStorelessService(t, f)
	todo := Todo{ID: "todo_2", Title: "本机", Origin: "conv1"}
	files := s.deliverTaskFiles(context.Background(), todo, "server", "server", "file: /tmp/a.md\n")
	if len(files) != 1 || files[0].Local != "" || files[0].local() != "/tmp/a.md" || files[0].Error != "" {
		t.Fatalf("files: %+v", files)
	}
}

func TestDeliverTaskFilesFailure(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["box"] = []proto.Session{{SessionID: "s1", MachineID: "box"}}
	newTaskFileFake(f, map[string][]byte{})
	s := newStorelessService(t, f)
	todo := Todo{ID: "todo_3", Title: "取回", Origin: "conv1"}
	files := s.deliverTaskFiles(context.Background(), todo, "box", "server", "file: /home/me/gone.md\n")
	if len(files) != 1 || files[0].Error == "" || files[0].Path != "/home/me/gone.md" {
		t.Fatalf("files: %+v", files)
	}
	if got := files[0].prompt(); got.Error == "" || got.Path != "/home/me/gone.md" {
		t.Fatalf("prompt: %+v", got)
	}
}

func TestDeliverTaskFilesOldNode(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["box"] = []proto.Session{{SessionID: "s1", MachineID: "box"}}
	ff := newTaskFileFake(f, map[string][]byte{"/home/me/a.md": []byte("a")})
	ff.upload = func(dest string) (string, error) {
		if dest == proto.UploadDestFile {
			return "", errors.New("unknown upload destination \"file\"")
		}
		return "", nil
	}
	f.answerNode("file.upload.end", func(string, json.RawMessage) (any, error) {
		return proto.UploadedFile{Path: "/home/me/.persona/uploads/terminal/2026-11-18/ab12-a.md"}, nil
	})
	s := newStorelessService(t, f)
	todo := Todo{ID: "todo_4", Title: "旧 node", Origin: "conv1"}
	files := s.deliverTaskFiles(context.Background(), todo, "box", "server", "file: /home/me/a.md\n")
	if len(files) != 1 || files[0].Local == "" || files[0].Error != "" {
		t.Fatalf("files: %+v", files)
	}
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if len(ff.uploads) != 2 || ff.uploads[0]["dest"] != proto.UploadDestFile || ff.uploads[1]["dest"] != proto.UploadDestTerminal {
		t.Fatalf("uploads: %+v", ff.uploads)
	}
}

// The task notice and a supervision message list each file as the Go code wrote it before the line moved into their
// templates (#10734): its copy, the original when there was nothing to copy, or why it did not come and where it stayed.
func TestTaskFileLinesInPrompts(t *testing.T) {
	long := strings.Repeat("x", 200)
	files := []taskFile{
		{Name: "manual.md", Path: "/home/me/manual.md", Local: "/x/uploads/files/2026-11-18/ab12-manual.md"},
		{Name: "a.md", Path: "/home/me/a.md"},
		{Name: "b.zip", Path: "/home/me/b.zip", Error: "over 100 MB"},
		{Name: "c.md", Path: "/home/me/c.md", Local: "/x/c.md", Error: "an earlier try"},
		{Name: "d.md", Path: "/home/me/d.md", Error: long},
	}
	want := []string{
		"manual.md：/x/uploads/files/2026-11-18/ab12-manual.md",
		"a.md：/home/me/a.md",
		"b.zip（没取回：over 100 MB；在原电脑 /home/me/b.zip）",
		"c.md：/x/c.md",
		"d.md（没取回：" + clip(long, 160) + "；在原电脑 /home/me/d.md）",
	}
	notice := renderNotice(&quietNotice{todo: Todo{Title: "整理手册"}, state: "done", files: files}, false, "")
	supervise := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{
		{ID: "todo_1", Title: "整理手册", Kind: "review", Files: taskFilePrompts(files)}}})
	for _, w := range want {
		if !strings.Contains(notice, "\n- "+w+"\n") {
			t.Errorf("notice lacks %q:\n%s", w, notice)
		}
		if !strings.Contains(supervise, "\n   - "+w+"\n") {
			t.Errorf("supervision message lacks %q:\n%s", w, supervise)
		}
	}
}
