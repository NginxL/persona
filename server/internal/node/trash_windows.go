//go:build windows

package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Deleting to the Recycle Bin (#9933). The shell does the delete (IFileOperation, the engine Explorer uses), so the bin's
// own rules apply: its quota, its per-drive folders, the bookkeeping that shows the item and puts it back. A progress
// sink watches it. Before each item (PreDeleteItem) the shell says whether it will recycle it
// (TSF_DELETE_RECYCLE_IF_POSSIBLE); when it won't (a network share, a drive with no Recycle Bin, the bin switched off,
// an item bigger than the bin), it is about to delete it for good, so the sink stops the operation there and nothing is
// deleted. After the item (PostDeleteItem) the shell gives its new place: <volume>\$Recycle.Bin\<user SID>\$R<id><ext>,
// with $I<id><ext> beside it holding the original path and the date. The trash_id keeps the $R name; Undo renames it
// back and removes the $I, which is what the bin itself does when it restores.

var (
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")

	clsidFileOperation           = windows.GUID{Data1: 0x3ad05575, Data2: 0x8857, Data3: 0x4850, Data4: [8]byte{0x92, 0x77, 0x11, 0xb8, 0x5b, 0xdb, 0x8e, 0x09}}
	iidFileOperation             = windows.GUID{Data1: 0x947aab5f, Data2: 0x0a5c, Data3: 0x4c13, Data4: [8]byte{0xb4, 0xd6, 0x4b, 0xf7, 0x83, 0x6f, 0xc9, 0xf8}}
	iidFileOperationProgressSink = windows.GUID{Data1: 0x04b0f1a7, Data2: 0x9490, Data3: 0x44bc, Data4: [8]byte{0x96, 0xe1, 0x42, 0x96, 0xa3, 0x12, 0x52, 0xe2}}
	iidShellItem                 = windows.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
	iidUnknown                   = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

const (
	clsctxAll = 0x17 // CLSCTX_ALL

	fofSilent           = 0x0004
	fofNoConfirmation   = 0x0010
	fofAllowUndo        = 0x0040
	fofNoConfirmMkdir   = 0x0200
	fofNoErrorUI        = 0x0400
	fofxRecycleOnDelete = 0x00080000
	fofxEarlyFailure    = 0x00100000

	tsfDeleteRecycleIfPossible = 0x80 // TRANSFER_SOURCE_FLAGS: PreDeleteItem's item goes to the Recycle Bin

	sigdnFileSysPath = 0x80058000

	hrOK          = 0
	hrNoInterface = 0x80004002
	hrAbort       = 0x80004004

	copyengineSharingViolationSrc = 0x80270027 // COPYENGINE_E_SHARING_VIOLATION_SRC: the item is open elsewhere
)

// errNoRecycleBin: the shell would have deleted the item for good, so it was left where it is.
var errNoRecycleBin = errors.New("no Recycle Bin")

// toRecycleBin is toTrash on Windows: real goes to the Recycle Bin, and the trash_id keeps the name the bin gave it.
func toRecycleBin(real, shown string) (string, error) {
	name, err := recycle(real)
	if errors.Is(err, errNoRecycleBin) && len(real) >= windows.MAX_PATH {
		return "", fmt.Errorf("%s can't go to the Recycle Bin, which takes paths of up to %d characters, so it was not deleted",
			shown, windows.MAX_PATH-1)
	}
	if errors.Is(err, errNoRecycleBin) {
		return "", fmt.Errorf("%s can't go to the Recycle Bin (it is on a network folder or a drive without one, or the "+
			"Recycle Bin is turned off or too small for it), so it was not deleted", shown)
	}
	if err != nil {
		return "", fmt.Errorf("%s could not go to the Recycle Bin: %w", shown, err)
	}
	return trashID{Name: name, Real: real, Shown: shown}.encode(), nil
}

type shellFileOpVtbl struct {
	QueryInterface, AddRef, Release                                          uintptr
	Advise, Unadvise, SetOperationFlags, SetProgressMessage                  uintptr
	SetProgressDialog, SetProperties, SetOwnerWindow, ApplyPropertiesToItem  uintptr
	ApplyPropertiesToItems, RenameItem, RenameItems, MoveItem, MoveItems     uintptr
	CopyItem, CopyItems, DeleteItem, DeleteItems, NewItem, PerformOperations uintptr
	GetAnyOperationsAborted                                                  uintptr
}

type shellFileOp struct{ vtbl *shellFileOpVtbl }

type shellItemVtbl struct {
	QueryInterface, AddRef, Release                                  uintptr
	BindToHandler, GetParent, GetDisplayName, GetAttributes, Compare uintptr
}

type shellItem struct{ vtbl *shellItemVtbl }

func (s *shellItem) release() { syscall.SyscallN(s.vtbl.Release, uintptr(unsafe.Pointer(s))) }

// path is the item's file system path ("" when it has none).
func (s *shellItem) path() string {
	var p *uint16
	if hr, _, _ := syscall.SyscallN(s.vtbl.GetDisplayName, uintptr(unsafe.Pointer(s)), sigdnFileSysPath, uintptr(unsafe.Pointer(&p))); failed(hr) || p == nil {
		return ""
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p)
}

// recycleSink is the IFileOperationProgressSink the delete reports to. It is one static object (COM keeps its address
// for the whole operation, and Go never moves a package variable); recycling holds recycleMu, so one delete uses it at
// a time, and what it saw is in recycleSaw.
type recycleSink struct{ vtbl *[19]uintptr }

var (
	recycleMu  sync.Mutex
	recycleSaw struct {
		sync.Mutex
		refused  bool    // an item the shell would not recycle
		posted   bool    // PostDeleteItem came
		hr       uintptr // its result
		recycled string  // where the item went
	}
	recycleSinkVtbl [19]uintptr
	theRecycleSink  = recycleSink{vtbl: &recycleSinkVtbl}
	recycleSinkOnce sync.Once
)

func makeRecycleSink() {
	ok1 := func(this uintptr) uintptr { return hrOK }
	ok2 := func(this, a uintptr) uintptr { return hrOK }
	ok3 := func(this, a, b uintptr) uintptr { return hrOK }
	ok4 := func(this, a, b, c uintptr) uintptr { return hrOK }
	ok5 := func(this, a, b, c, d uintptr) uintptr { return hrOK }
	ok6 := func(this, a, b, c, d, e uintptr) uintptr { return hrOK }
	ok7 := func(this, a, b, c, d, e, f uintptr) uintptr { return hrOK }
	ok8 := func(this, a, b, c, d, e, f, g uintptr) uintptr { return hrOK }
	queryInterface := func(this uintptr, riid *windows.GUID, out *uintptr) uintptr {
		if *riid == iidUnknown || *riid == iidFileOperationProgressSink {
			*out = this
			return hrOK
		}
		*out = 0
		return hrNoInterface
	}
	preDelete := func(this, flags uintptr, item *shellItem) uintptr {
		if flags&tsfDeleteRecycleIfPossible != 0 {
			return hrOK
		}
		recycleSaw.Lock()
		recycleSaw.refused = true
		recycleSaw.Unlock()
		return hrAbort
	}
	postDelete := func(this, flags uintptr, item *shellItem, hr uintptr, created *shellItem) uintptr {
		var at string
		if created != nil {
			at = created.path()
		}
		recycleSaw.Lock()
		recycleSaw.posted, recycleSaw.hr, recycleSaw.recycled = true, hr, at
		recycleSaw.Unlock()
		return hrOK
	}
	cb := windows.NewCallback
	recycleSinkVtbl = [19]uintptr{
		cb(queryInterface), cb(ok1), cb(ok1), // IUnknown: a static object, so AddRef and Release count nothing
		cb(ok1), cb(ok2), // StartOperations, FinishOperations
		cb(ok4), cb(ok6), // PreRenameItem, PostRenameItem
		cb(ok5), cb(ok7), // PreMoveItem, PostMoveItem
		cb(ok5), cb(ok7), // PreCopyItem, PostCopyItem
		cb(preDelete), cb(postDelete),
		cb(ok4), cb(ok8), // PreNewItem, PostNewItem
		cb(ok3),                   // UpdateProgress
		cb(ok1), cb(ok1), cb(ok1), // ResetTimer, PauseTimer, ResumeTimer
	}
}

// recycle moves the file or folder at path (absolute; a link is recycled as the link) into the Recycle Bin and returns
// its name there ($R…). errNoRecycleBin: the shell would have deleted it for good, and it is left where it is.
func recycle(path string) (string, error) {
	if err := procCoCreateInstance.Find(); err != nil {
		return "", err
	}
	if err := procSHCreateItemFromParsingName.Find(); err != nil {
		return "", err
	}
	recycleSinkOnce.Do(makeRecycleSink)
	recycleMu.Lock()
	defer recycleMu.Unlock()
	recycleSaw.Lock()
	recycleSaw.refused, recycleSaw.posted, recycleSaw.hr, recycleSaw.recycled = false, false, 0, ""
	recycleSaw.Unlock()

	done := make(chan shellDeleteResult, 1)
	go func() {
		// IFileOperation wants a single-threaded apartment. The thread is never unlocked, so it ends with this goroutine
		// and no other code runs on it with COM set up.
		runtime.LockOSThread()
		done <- shellDelete(path)
	}()
	r := <-done
	if r.err != nil {
		return "", r.err
	}
	recycleSaw.Lock()
	refused, posted, hrItem, recycled := recycleSaw.refused, recycleSaw.posted, recycleSaw.hr, recycleSaw.recycled
	recycleSaw.Unlock()
	// An item's result is a success code too when it was recycled (COPYENGINE_S_DONT_PROCESS_CHILDREN for a file as for
	// a folder), so failures are told by their sign, never by != S_OK.
	switch {
	case refused:
		return "", errNoRecycleBin
	case posted && failed(hrItem):
		return "", hresultError(hrItem)
	case failed(r.hr):
		return "", hresultError(r.hr)
	case !posted || r.aborted:
		return "", errors.New("the shell stopped the delete")
	case recycled == "":
		return "", errors.New("the shell deleted it without saying where in the Recycle Bin it went, so Undo can't put it back")
	}
	name := filepath.Base(recycled)
	if dir, err := recycleBinDir(path); err != nil || !strings.EqualFold(dir, filepath.Dir(recycled)) || !isRecycledName(name) {
		// Undo would not find it there: it goes straight back, so the failed delete changes nothing.
		if err := unrecycle(recycled, path); err != nil {
			return "", fmt.Errorf("it went to the Recycle Bin as %s, where Undo can't find it, and could not be put back: %w", recycled, err)
		}
		return "", fmt.Errorf("the Recycle Bin took it as %s, where Undo can't find it, so it was put back", recycled)
	}
	return name, nil
}

// unrecycle moves item, a $R… in a Recycle Bin folder, back to to (never over anything there) and removes its $I.
func unrecycle(item, to string) error {
	if err := renameExcl(item, to); err != nil {
		return err
	}
	if name := filepath.Base(item); isRecycledName(name) {
		_ = os.Remove(filepath.Join(filepath.Dir(item), "$I"+name[2:]))
	}
	return nil
}

func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

type shellDeleteResult struct {
	hr      uintptr // PerformOperations' result, or the call that failed before it
	aborted bool
	err     error
}

// shellDelete deletes path with IFileOperation, the progress going to theRecycleSink, on the calling thread, which it
// makes a single-threaded apartment; everything is released when it returns.
func shellDelete(path string) shellDeleteResult {
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil {
		return shellDeleteResult{err: fmt.Errorf("COM: %w", err)}
	}
	defer windows.CoUninitialize()
	var op *shellFileOp
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOperation)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidFileOperation)), uintptr(unsafe.Pointer(&op))); failed(hr) {
		return shellDeleteResult{err: fmt.Errorf("the shell's file operation: %w", hresultError(hr))}
	}
	self := uintptr(unsafe.Pointer(op))
	defer syscall.SyscallN(op.vtbl.Release, self)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return shellDeleteResult{err: err}
	}
	var item *shellItem
	if hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item))); failed(hr) {
		return shellDeleteResult{hr: hr}
	}
	defer item.release()
	flags := uintptr(fofAllowUndo | fofxRecycleOnDelete | fofSilent | fofNoConfirmation | fofNoConfirmMkdir | fofNoErrorUI | fofxEarlyFailure)
	if hr, _, _ := syscall.SyscallN(op.vtbl.SetOperationFlags, self, flags); failed(hr) {
		return shellDeleteResult{hr: hr}
	}
	if hr, _, _ := syscall.SyscallN(op.vtbl.DeleteItem, self, uintptr(unsafe.Pointer(item)), uintptr(unsafe.Pointer(&theRecycleSink))); failed(hr) {
		return shellDeleteResult{hr: hr}
	}
	hr, _, _ := syscall.SyscallN(op.vtbl.PerformOperations, self)
	var aborted int32
	syscall.SyscallN(op.vtbl.GetAnyOperationsAborted, self, uintptr(unsafe.Pointer(&aborted)))
	return shellDeleteResult{hr: hr, aborted: aborted != 0}
}

// isRecycledName: a name the Recycle Bin gives what it keeps ($R…), with nothing that would leave its folder.
func isRecycledName(name string) bool {
	return len(name) > 2 && strings.EqualFold(name[:2], "$R") && !strings.ContainsAny(name, `\/:`) && name != "." && name != ".."
}

// recycleBinDir is this user's Recycle Bin folder on the drive (volume) path is on: <volume>\$Recycle.Bin\<SID>.
func recycleBinDir(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, len(path)+windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return "", fmt.Errorf("the drive of %s: %w", path, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return filepath.Join(windows.UTF16ToString(buf), "$Recycle.Bin", user.User.Sid.String()), nil
}

// recycledAt is where the Recycle Bin keeps name, which was real, and the $I file that goes with it.
func recycledAt(real, name string) (item, info string, err error) {
	if !isRecycledName(name) {
		return "", "", fmt.Errorf("%q is not a name in the Recycle Bin", name)
	}
	dir, err := recycleBinDir(real)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, name), filepath.Join(dir, "$I"+name[2:]), nil
}

// hresultError words a failed HRESULT: a Win32 error as Windows words it (and errors.Is finds it), others by number.
func hresultError(hr uintptr) error {
	switch {
	case hr&0xffff0000 == 0x80070000:
		return syscall.Errno(hr & 0xffff)
	case hr == copyengineSharingViolationSrc:
		return errors.New("it is open in another program")
	}
	return fmt.Errorf("error 0x%08X", uint32(hr))
}
