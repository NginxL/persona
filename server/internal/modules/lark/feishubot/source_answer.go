package feishubot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// AnswerContent makes one reply containing the entire answer.
func (c *Client) AnswerContent(ctx context.Context, text string) (msgType, content string, err error) {
	if content, ok := InlineAnswer(text); ok {
		return TypePost, content, nil
	}
	if len(text) > 30<<20 {
		return "", "", &APIError{Op: "upload answer", Msg: "answer exceeds the Feishu file limit"}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("file_type", "stream"); err != nil {
		return "", "", err
	}
	if err := form.WriteField("file_name", "persona-answer.txt"); err != nil {
		return "", "", err
	}
	part, err := form.CreateFormFile("file", "persona-answer.txt")
	if err != nil {
		return "", "", err
	}
	if _, err := io.WriteString(part, text); err != nil {
		return "", "", err
	}
	if err := form.Close(); err != nil {
		return "", "", err
	}
	var out struct {
		Key string `json:"file_key"`
	}
	if err := c.callBody(ctx, "upload answer", http.MethodPost, "/open-apis/im/v1/files", body.Bytes(), form.FormDataContentType(), &out, false); err != nil {
		return "", "", err
	}
	if out.Key == "" {
		return "", "", fmt.Errorf("feishu upload answer: no file key")
	}
	reply, _ := json.Marshal(map[string]string{"file_key": out.Key})
	return TypeFile, string(reply), nil
}
