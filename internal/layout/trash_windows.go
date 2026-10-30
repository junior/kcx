//go:build windows

package layout

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The shell's file operation, as Explorer's Delete uses it: FO_DELETE with
// FOF_ALLOWUNDO moves to the Recycle Bin; no confirmation, no progress window,
// no error dialog.
const (
	foDelete          = 0x0003
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

// shFileOpStruct is SHFILEOPSTRUCTW in its 64-bit layout: natural alignment,
// as shellapi.h declares it outside 32-bit builds (kcx ships amd64 and arm64).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// shell32 is loaded from the system directory only, never from the search path.
var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// platformTrash moves paths to the Recycle Bin.
func platformTrash(paths []string) (handled bool, err error) {
	// pFrom is a list of absolute paths, each ending in a NUL, and the list in
	// a second one.
	var from []uint16
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return true, err
		}
		u, err := windows.UTF16FromString(abs)
		if err != nil {
			return true, err
		}
		from = append(from, u...)
	}
	from = append(from, 0)
	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return true, fmt.Errorf("the Recycle Bin refused the move (SHFileOperation error %#x)", r)
	}
	if op.fAnyOperationsAborted != 0 {
		return true, errors.New("the move to the Recycle Bin was aborted")
	}
	return true, nil
}
