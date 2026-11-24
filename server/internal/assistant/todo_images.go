package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// TodoImage is the metadata an app needs to draw a to-do's picture and fetch its bytes privately. In particular it
// never carries Data or a path on the manager or target machine.
type TodoImage struct {
	ID     string `json:"id"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URL    string `json:"url"`
	Name   string `json:"name,omitempty"`
	Size   int    `json:"size,omitempty"`
}

type todoImageStore interface {
	TodoImageContent(context.Context, string, string) (db.TodoImage, error)
	TodoImageContents(context.Context, string) ([]db.TodoImage, error)
}

var _ todoImageStore = (*db.DB)(nil)

type todoImageInputJSON struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
	Data string `json:"data"`
	Name string `json:"name"`
}

// parseTodoImages applies the meeting-photo input rules before a to-do is created. The status distinguishes malformed
// images (400) from decoded-size and count limits (413); DecodeConfig bounds pixels before the complete Decode.
// The limits of a to-do's image, those of a meeting's photo (the recording module's meeting_photos.go).
const (
	maxPhotoBytes = 1 << 20 // its image, decoded
	maxPhotoSide  = 4096    // pixels on the image's long side
)

func parseTodoImages(in []todoImageInputJSON) ([]db.TodoImage, int, error) {
	if len(in) > kb.MaxPhotos {
		return nil, http.StatusRequestEntityTooLarge, i18n.Err("assistant-meeting.too_many_images", i18n.Args{"max": kb.MaxPhotos})
	}
	out := make([]db.TodoImage, 0, len(in))
	seen := map[string]bool{}
	for _, row := range in {
		id := row.ID
		if id == "" {
			id = newTodoImageID()
		} else {
			var ok bool
			if id, ok = kb.RecordingID(id); !ok {
				return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_ask_id")
			}
		}
		if seen[id] {
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.photo_id_reused")
		}
		seen[id] = true
		if row.Mime != kb.PhotoJPEG && row.Mime != kb.PhotoPNG {
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_photo_mime")
		}
		data, err := base64.StdEncoding.DecodeString(row.Data)
		switch {
		case err != nil || len(data) == 0:
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_photo_data")
		case len(data) > maxPhotoBytes:
			return nil, http.StatusRequestEntityTooLarge, i18n.Err("assistant-meeting.image_too_large", i18n.Args{"size": "1 MiB"})
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || "image/"+format != row.Mime || cfg.Width <= 0 || cfg.Height <= 0 {
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_photo_data")
		}
		if max(cfg.Width, cfg.Height) > maxPhotoSide {
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.photo_too_wide", i18n.Args{"max": maxPhotoSide})
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return nil, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_photo_data")
		}
		out = append(out, db.TodoImage{ID: id, Mime: row.Mime, Width: cfg.Width, Height: cfg.Height,
			Name: row.Name, Size: len(data), Data: data})
	}
	return out, 0, nil
}

// newTodoImageID makes a UUID when an additive client did not supply one. randomHex is 128 bits from crypto/rand.
func newTodoImageID() string {
	raw := randomHex(16)
	raw = raw[:12] + "4" + raw[13:16] + "8" + raw[17:]
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
}

// todoImageMetadata converts stored rows to their API metadata and retains bytes only for the storeless service.
func todoImageMetadata(todoID string, rows []db.TodoImage) ([]TodoImage, map[string][]byte) {
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]TodoImage, 0, len(rows))
	var contents map[string][]byte
	for _, row := range rows {
		size := row.Size
		if size == 0 {
			size = len(row.Data)
		}
		out = append(out, TodoImage{ID: row.ID, Mime: row.Mime, Width: row.Width, Height: row.Height,
			URL:  "/api/v1/todos/" + url.PathEscape(todoID) + "/images/" + url.PathEscape(row.ID),
			Name: row.Name, Size: size})
		if len(row.Data) > 0 {
			if contents == nil {
				contents = map[string][]byte{}
			}
			contents[row.ID] = row.Data
		}
	}
	return out, contents
}

func (s *Service) todoImageRoutes() {
	s.handle("GET /api/v1/todos/{id}/images/{image}", func(w http.ResponseWriter, r *http.Request) {
		// Answer devices may draw the same to-do as full ones. An extension gets no media scope (route_scopes.txt).
		if !fullRole(r) && r.Header.Get(RoleHeader) != "answer" {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-todos.images_forbidden"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		img, err := s.todoImageContent(r.Context(), r.PathValue("id"), r.PathValue("image"))
		if errors.Is(err, db.ErrTodoImageNotFound) {
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-todos.not_found"))
			return
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", img.Mime)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "private, no-store")
		_, _ = w.Write(img.Data)
	})
}

func (s *Service) todoImageContent(ctx context.Context, todoID, imageID string) (db.TodoImage, error) {
	if st, ok := s.store().(todoImageStore); ok {
		return st.TodoImageContent(ctx, todoID, imageID)
	}
	t, ok := s.todoNow(todoID)
	if !ok {
		return db.TodoImage{}, db.ErrTodoImageNotFound
	}
	for _, meta := range t.Images {
		if meta.ID == imageID {
			data, ok := t.imageData[imageID]
			if !ok {
				return db.TodoImage{}, db.ErrTodoImageNotFound
			}
			return db.TodoImage{TodoID: todoID, ID: meta.ID, Mime: meta.Mime, Width: meta.Width, Height: meta.Height,
				Name: meta.Name, Size: len(data), Data: data}, nil
		}
	}
	return db.TodoImage{}, db.ErrTodoImageNotFound
}

func (s *Service) todoImageContents(ctx context.Context, t Todo) ([]db.TodoImage, error) {
	if len(t.Images) == 0 {
		return nil, nil
	}
	if st, ok := s.store().(todoImageStore); ok {
		return st.TodoImageContents(ctx, t.ID)
	}
	out := make([]db.TodoImage, 0, len(t.Images))
	for _, meta := range t.Images {
		data, ok := t.imageData[meta.ID]
		if !ok {
			return nil, db.ErrTodoImageNotFound
		}
		out = append(out, db.TodoImage{TodoID: t.ID, ID: meta.ID, Mime: meta.Mime, Width: meta.Width, Height: meta.Height,
			Name: meta.Name, Size: len(data), Data: data})
	}
	return out, nil
}

func todoProtoImages(rows []db.TodoImage) []proto.Image {
	if len(rows) == 0 {
		return nil
	}
	out := make([]proto.Image, 0, len(rows))
	for _, row := range rows {
		out = append(out, proto.Image{Mime: row.Mime, Data: base64.StdEncoding.EncodeToString(row.Data),
			Size: len(row.Data), Width: row.Width, Height: row.Height})
	}
	return out
}
