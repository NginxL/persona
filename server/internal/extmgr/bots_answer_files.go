package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"golang.org/x/sync/singleflight"
)

const (
	botAnswerFilesKey = "bots.feishu.answer_files"
	// A source report can be retried for seven days after the todo closes.
	botAnswerFileKeep = 7 * 24 * time.Hour
)

// errBotRestarted: the bot client changed while a long answer was prepared. It has no Feishu
// code, so the assistant treats it as failing for now (feishubot.Transient) and tries again.
var errBotRestarted = errors.New("feishu: the bot restarted while the answer was prepared")

type botAnswerFile struct {
	Content   string    `json:"content"`
	ExpiresAt time.Time `json:"expires_at"`
}

type botAnswerFiles struct {
	uploads singleflight.Group
	mu      sync.Mutex
	files   map[string]botAnswerFile
}

// A delivery retry must replay the same UUID and reply body, including the file_key.
// The upload is saved before attempting the reply, whose result may be lost.
func (h *Manager) feishuAnswerContent(ctx context.Context, api *feishubot.Client, parent, uuid, text string) (string, string, error) {
	if content, inline := feishubot.InlineAnswer(text); inline {
		return feishubot.TypePost, content, nil
	}
	digest := sha256.Sum256([]byte(text))
	keyParts := []string{api.Domain(), api.AppID(), parent, uuid, hex.EncodeToString(digest[:])}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(keyParts, "\x00"))))
	files := &h.bots.answerFiles
	content, err, _ := files.uploads.Do(key, func() (any, error) {
		cached, err := h.cachedBotAnswerFile(ctx, key)
		if err != nil || cached != "" {
			return cached, err
		}
		_, prepared, err := api.AnswerContent(ctx, text)
		if err != nil {
			return "", err
		}
		return prepared, h.saveBotAnswerFile(ctx, key, prepared)
	})
	if err != nil {
		return "", "", err
	}
	return feishubot.TypeFile, content.(string), nil
}

func (h *Manager) cachedBotAnswerFile(ctx context.Context, key string) (string, error) {
	files := &h.bots.answerFiles
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.files == nil {
		var saved map[string]botAnswerFile
		if h.store != nil {
			if _, err := h.store.Setting(ctx, botAnswerFilesKey, &saved); err != nil {
				return "", fmt.Errorf("load Feishu answer files: %w", err)
			}
		}
		if saved == nil {
			saved = make(map[string]botAnswerFile)
		}
		files.files = saved
	}
	entry := files.files[key]
	if h.bots.clock().Before(entry.ExpiresAt) {
		return entry.Content, nil
	}
	return "", nil
}

func (h *Manager) saveBotAnswerFile(ctx context.Context, key, content string) error {
	files := &h.bots.answerFiles
	files.mu.Lock()
	defer files.mu.Unlock()
	now := h.bots.clock()
	maps.DeleteFunc(files.files, func(_ string, file botAnswerFile) bool { return !now.Before(file.ExpiresAt) })
	files.files[key] = botAnswerFile{Content: content, ExpiresAt: now.Add(botAnswerFileKeep)}
	if h.store != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := h.store.PutSetting(ctx, botAnswerFilesKey, files.files); err != nil {
			delete(files.files, key)
			return fmt.Errorf("save Feishu answer file: %w", err)
		}
	}
	return nil
}
