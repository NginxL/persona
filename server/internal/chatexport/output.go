package chatexport

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/i18n"
)

var errOutputExists = errors.New("export destination already exists")

type destination struct {
	root *os.Root
	name string
}

func openDestination(path string) (destination, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	if name == "" || name == "." || name == ".." {
		return destination{}, fs.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return destination{}, err
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		root.Close()
		if err == nil {
			err = errOutputExists
		}
		return destination{}, err
	}
	return destination{root, name}, nil
}

// publish links a synced private inode into place. The link is the only publication
// point and cannot replace an existing file, even if another writer won the race.
func (d destination) publish(ctx context.Context, write func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary := ".persona-chat-" + rand.Text()
	file, err := d.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer d.root.Remove(temporary)
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.root.Link(temporary, d.name); errors.Is(err, fs.ErrExist) {
		return errOutputExists
	} else {
		return err
	}
}

func exportError(lang i18n.Lang, err error) error {
	switch {
	case errors.Is(err, errOutputExists):
		return errors.New(i18n.T(lang, "chatexport.exists"))
	case errors.Is(err, context.Canceled):
		return errors.New(i18n.T(lang, "chatexport.canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New(i18n.T(lang, "chatexport.timed_out"))
	default:
		return errors.New(i18n.T(lang, "chatexport.output_failed"))
	}
}
