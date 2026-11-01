//go:build windows

package layout

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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

// platformTrash moves paths to the Recycle Bin. The shell's delete removes a
// file for good, without a word, where its drive has no Recycle Bin, so each
// path is checked first, and none moves when one would be deleted.
func platformTrash(paths []string) (handled bool, err error) {
	for _, p := range paths {
		if why := noRecycleBin(p); why != "" {
			return true, fmt.Errorf("%s cannot go to the Recycle Bin: %s, so the move would delete it for good; move or delete it yourself", p, why)
		}
	}
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

// binFacts are what decides whether a file's drive has a Recycle Bin.
type binFacts struct {
	driveType uint32 // GetDriveType of the volume the file is on
	subst     bool   // a drive letter that subst made for a folder
	off       bool   // the volume's Recycle Bin is turned off (NukeOnDelete)
	policy    bool   // a policy turns the Recycle Bin off (NoRecycleFiles)
}

// why is the reason the file cannot go to the Recycle Bin, or "" when it can.
// Only a fixed drive has one; a network drive (a home folder on a share, say),
// a removable one and a subst drive do not.
func (f binFacts) why() string {
	switch {
	case f.subst:
		return "it is on a subst drive"
	case f.driveType == windows.DRIVE_REMOTE:
		return "it is on a network drive"
	case f.driveType == windows.DRIVE_REMOVABLE:
		return "it is on a removable drive"
	case f.driveType == windows.DRIVE_CDROM:
		return "it is on an optical drive"
	case f.driveType == windows.DRIVE_RAMDISK:
		return "it is on a RAM disk"
	case f.driveType != windows.DRIVE_FIXED:
		return "kcx cannot tell what kind of drive it is on"
	case f.policy:
		return "a policy turns the Recycle Bin off"
	case f.off:
		return "the Recycle Bin of its drive is turned off"
	}
	return ""
}

// noRecycleBin says why path cannot go to the Recycle Bin, or "" when it can.
func noRecycleBin(path string) string {
	f, err := binFactsOf(path)
	if err != nil {
		return "kcx cannot tell which drive it is on (" + err.Error() + ")"
	}
	return f.why()
}

// binFactsOf finds the facts for path. The drive's kind is what decides; the
// registry settings are read where they are, and a setting kcx cannot read
// counts as not set.
func binFactsOf(path string) (binFacts, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return binFacts{}, err
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return binFacts{}, err
	}
	buf := make([]uint16, len(abs)+windows.MAX_PATH)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return binFacts{}, err
	}
	root := windows.UTF16ToString(buf) // C:\ or \\server\share\, with the backslash
	r, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return binFacts{}, err
	}
	f := binFacts{driveType: windows.GetDriveType(r)}
	if len(root) == 3 && root[1] == ':' {
		// subst maps a letter to a folder: QueryDosDevice says \??\ then the folder.
		target := make([]uint16, windows.MAX_PATH)
		letter, _ := windows.UTF16PtrFromString(root[:2])
		if n, err := windows.QueryDosDevice(letter, &target[0], uint32(len(target))); err == nil && n > 0 {
			f.subst = strings.HasPrefix(windows.UTF16ToString(target), `\??\`)
		}
	}
	const policies = `Software\Microsoft\Windows\CurrentVersion\Policies\Explorer`
	f.policy = regFlag(registry.CURRENT_USER, policies, "NoRecycleFiles") || regFlag(registry.LOCAL_MACHINE, policies, "NoRecycleFiles")
	vol := make([]uint16, windows.MAX_PATH)
	if err := windows.GetVolumeNameForVolumeMountPoint(r, &vol[0], uint32(len(vol))); err == nil {
		// \\?\Volume{GUID}\: Explorer keeps the volume's settings under its GUID.
		name := windows.UTF16ToString(vol)
		if i, j := strings.IndexByte(name, '{'), strings.IndexByte(name, '}'); i >= 0 && j > i {
			f.off = regFlag(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume\`+name[i:j+1], "NukeOnDelete")
		}
	}
	return f, nil
}

// regFlag says whether the DWORD name under root\path is 1.
func regFlag(root registry.Key, path, name string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return err == nil && v == 1
}
