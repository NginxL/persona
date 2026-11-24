package feishubot

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInlineAnswerUsesLiteralText(t *testing.T) {
	text := "The notes are ready.\n\n- Fixed a crash\n- <at user_id=\"all\">everyone</at>"
	raw, ok := InlineAnswer(text)
	var post map[string]struct {
		Content [][]map[string]string
	}
	if err := json.Unmarshal([]byte(raw), &post); err != nil || !ok {
		t.Fatalf("inline fallback: %v, %v", ok, err)
	}
	rows := post["zh_cn"].Content
	want := strings.Split(strings.ReplaceAll(text, "\n\n", "\n\u00a0\n"), "\n")
	if len(rows) != len(want) {
		t.Fatalf("post paragraphs: %v", rows)
	}
	for i, row := range rows {
		if len(row) != 1 || row[0]["tag"] != "text" || row[0]["text"] != want[i] {
			t.Fatalf("post changed the answer or activated markup: %v", row)
		}
	}
}

func TestLongAnswerUploadedWhole(t *testing.T) {
	text := strings.Repeat("一段答复带有\n- 列表\n\n", 3000)
	c, _, hits, _ := retryAPIClient(t, func(w http.ResponseWriter, r *http.Request, _ *atomic.Int32) {
		if r.Method != http.MethodPost || r.URL.Path != "/open-apis/im/v1/files" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected upload request: %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("bad multipart upload: %v", err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file: %v", err)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || string(got) != text || r.FormValue("file_type") != "stream" || header.Filename != "persona-answer.txt" {
			t.Errorf("file payload differs: %d bytes, %v", len(got), err)
		}
		okJSON(w, map[string]any{"code": 0, "data": map[string]string{"file_key": "file_1"}})
	})
	msgType, content, err := c.AnswerContent(t.Context(), text)
	if err != nil || hits.Load() != 1 || msgType != TypeFile || content != `{"file_key":"file_1"}` {
		t.Fatalf("want one file reply: %s %s, %v (%d uploads)", msgType, content, err, hits.Load())
	}
}
