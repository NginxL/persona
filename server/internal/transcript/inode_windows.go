package transcript

import (
	"encoding/binary"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// identifiedFileInfo caches the opened file's identity once per stat, never once per line.
// Windows FileInfo.Sys exposes timestamps but not a file index; creation time cannot identify
// replacements because NTFS may preserve it when another file takes the same name.
type identifiedFileInfo struct {
	os.FileInfo
	id uint64
}

func inode(st os.FileInfo) uint64 {
	if identified, ok := st.(identifiedFileInfo); ok {
		return identified.id
	}
	return 0
}

func statTranscript(path string) (os.FileInfo, error) {
	f, err := openTranscriptHandle(path, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return statOpenedTranscript(f)
}

func statOpenedTranscript(f *os.File) (os.FileInfo, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return nil, &os.PathError{Op: "stat", Path: f.Name(), Err: err}
	}
	// Include the volume as well as the index, while retaining the portable uint64 history token.
	var identity [12]byte
	binary.LittleEndian.PutUint32(identity[0:4], info.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(identity[4:8], info.FileIndexHigh)
	binary.LittleEndian.PutUint32(identity[8:12], info.FileIndexLow)
	h := fnv.New64a()
	_, _ = h.Write(identity[:])
	id := h.Sum64()
	if id == 0 {
		id = 1 // zero means that no identity was recorded yet
	}
	return identifiedFileInfo{FileInfo: st, id: id}, nil
}

func openTranscript(path string) (*os.File, error) {
	return openTranscriptHandle(path, windows.GENERIC_READ)
}

func openTranscriptHandle(path string, access uint32) (*os.File, error) {
	if path == "" {
		return nil, &os.PathError{Op: "open", Path: path, Err: windows.ERROR_FILE_NOT_FOUND}
	}
	name, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Match os.Open's support for long paths, including network shares.
	if len(name) >= 248 && !strings.HasPrefix(name, `\\?\`) && !strings.HasPrefix(name, `\\.\`) {
		if strings.HasPrefix(name, `\\`) {
			name = `\\?\UNC\` + name[2:]
		} else {
			name = `\\?\` + name
		}
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Allow another process to move or delete this path while this reader keeps its snapshot.
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
