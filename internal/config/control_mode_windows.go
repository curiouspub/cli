//go:build windows

package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func controlSecurity() (*windows.SecurityAttributes, *windows.SID, error) {
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return nil, nil, errors.New("cannot identify Control state owner")
	}
	sid := user.User.Sid
	sd, e := windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;OICI;FA;;;" + sid.String() + ")")
	if e != nil {
		return nil, nil, errors.New("cannot establish Control permissions")
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, sid, nil
}
func controlVerify(h windows.Handle, sid *windows.SID, directory bool) error {
	var info windows.ByHandleFileInformation
	if e := windows.GetFileInformationByHandle(h, &info); e != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && info.NumberOfLinks != 1) {
		return errors.New("unsafe Control state path")
	}
	sd, e := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil || sd == nil {
		return errors.New("cannot verify Control permissions")
	}
	owner, _, e := sd.Owner()
	if e != nil || owner == nil || !owner.Equals(sid) {
		return errors.New("unsafe Control state owner")
	}
	ctl, _, e := sd.Control()
	if e != nil || ctl&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Control permissions are not protected")
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil || acl.AceCount != 1 {
		return errors.New("unsafe Control permissions")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if e = windows.GetAce(acl, 0, &ace); e != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errors.New("unsafe Control permissions")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(sid) {
		return errors.New("unsafe Control permissions")
	}
	return nil
}
func controlWindowsOpen(path string, access, creation, flags uint32) (*os.File, error) {
	sa, sid, e := controlSecurity()
	if e != nil {
		return nil, e
	}
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return nil, e
	}
	h, e := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, sa, creation, flags|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if e != nil {
		return nil, e
	}
	if e = controlVerify(h, sid, flags&windows.FILE_FLAG_BACKUP_SEMANTICS != 0); e != nil {
		_ = windows.CloseHandle(h)
		return nil, e
	}
	return os.NewFile(uintptr(h), path), nil
}

// controlDriveRoot admits only an ordinary drive-letter root spelling.
func controlDriveRoot(path string) bool {
	return len(path) == 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && path[2] == '\\'
}

// controlNativeFixedRoot binds the root identity to the opened directory.
// A volume GUID path must name the root itself, not a directory behind a
// substituted drive, and both the mount and that same volume must be fixed.
func controlNativeFixedRoot(path string, handle windows.Handle) bool {
	if !controlDriveRoot(path) {
		return false
	}
	root, err := windows.UTF16PtrFromString(path)
	if err != nil || windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return false
	}
	var mount [1024]uint16
	if err = windows.GetVolumeNameForVolumeMountPoint(root, &mount[0], uint32(len(mount))); err != nil {
		return false
	}
	var final [1024]uint16
	const volumeNameGUID = 1
	n, err := windows.GetFinalPathNameByHandle(handle, &final[0], uint32(len(final)), volumeNameGUID)
	if err != nil || n == 0 || n >= uint32(len(final)) {
		return false
	}
	actual, expected := windows.UTF16ToString(final[:n]), windows.UTF16ToString(mount[:])
	guid, err := windows.UTF16PtrFromString(actual)
	if err != nil || !controlRootNamesAgree(actual, expected, windows.GetDriveType(root), windows.GetDriveType(guid)) {
		return false
	}
	// Compare the current DOS-device targets for the drive and the volume
	// with this same handle's native device-root path. Redirected drive
	// aliases (including an alias to a volume root) do not qualify.
	driveTarget, err := controlDOSDevice(path[:2])
	if err != nil {
		return false
	}
	volumeTarget, err := controlDOSDevice(actual[4 : len(actual)-1])
	if err != nil {
		return false
	}
	var native [1024]uint16
	const volumeNameNT = 2
	n, err = windows.GetFinalPathNameByHandle(handle, &native[0], uint32(len(native)), volumeNameNT)
	if err != nil || n == 0 || n >= uint32(len(native)) {
		return false
	}
	return controlDeviceNamesAgree(driveTarget, volumeTarget, windows.UTF16ToString(native[:n]))
}
func controlDOSDevice(name string) (string, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	var target [1024]uint16
	n, err := windows.QueryDosDevice(ptr, &target[0], uint32(len(target)))
	if err != nil || n == 0 || n >= uint32(len(target)) {
		return "", errors.New("cannot verify Control volume")
	}
	first := windows.UTF16ToString(target[:n])
	if first == "" {
		return "", errors.New("cannot verify Control volume")
	}
	return first, nil
}
func controlDeviceNamesAgree(drive, volume, handle string) bool {
	return drive != "" && !strings.HasSuffix(drive, `\`) && strings.EqualFold(drive, volume) && strings.EqualFold(handle, drive+`\`)
}
func controlRootNamesAgree(actual, expected string, pathType, volumeType uint32) bool {
	return pathType == windows.DRIVE_FIXED && volumeType == windows.DRIVE_FIXED && controlGUIDRoot(actual) && strings.EqualFold(actual, expected)
}
func controlGUIDRoot(value string) bool {
	const prefix = `\\?\Volume{`
	if len(value) != len(prefix)+36+2 || !strings.EqualFold(value[:len(prefix)], prefix) || value[len(value)-2:] != `}\` {
		return false
	}
	for i, ch := range value[len(prefix) : len(prefix)+36] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}
func controlTrustedParent(s, current *windows.SID) bool {
	return s != nil && ((current != nil && s.Equals(current)) || s.IsWellKnown(windows.WinLocalSystemSid) || s.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

// controlInstallerSID constructs the service identity using native SID fields.
// Copy it before releasing the Windows allocation so callers own its bytes.
func controlInstallerSID() (*windows.SID, error) {
	var native *windows.SID
	if err := windows.AllocateAndInitializeSid(&windows.SECURITY_NT_AUTHORITY, 6, 80, 956008885, 3418522649, 1831038044, 1853292631, 2271478464, 0, 0, &native); err != nil {
		return nil, err
	}
	if native == nil {
		return nil, errors.New("cannot identify Control parent")
	}
	defer windows.FreeSid(native)
	return native.Copy()
}
func controlParentOwnerAllowed(owner, current *windows.SID, fixedRoot bool) bool {
	if controlTrustedParent(owner, current) {
		return true
	}
	if !fixedRoot || owner == nil {
		return false
	}
	service, err := controlInstallerSID()
	return err == nil && owner.Equals(service)
}
func controlRootCreateACE(who *windows.SID, flags byte, mask uint32, fixedRoot bool) bool {
	return fixedRoot && who != nil && who.IsWellKnown(windows.WinAuthenticatedUserSid) && flags == 0 && mask == 0x4
}

// controlParent obtains path attributes, owner and DACL from the same opened
// directory object. This prevents a competing child creation from mixing a
// safe name's attributes with a different object's security descriptor.
func controlParent(path string, sid *windows.SID) error {
	name, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return errors.New("unsafe Control parent")
	}
	h, e := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if e != nil {
		return errors.New("unsafe Control parent")
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(h, &info); e != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("unsafe Control parent")
	}
	fixedRoot := false
	if controlDriveRoot(path) {
		fixedRoot = controlNativeFixedRoot(path, h)
	}
	sd, e := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil || sd == nil {
		return errors.New("cannot verify Control parent")
	}
	return controlParentDescriptor(sd, sid, fixedRoot)
}

// controlParentDescriptor applies the same policy to the descriptor read
// from the opened parent. The root fact is derived from that same handle.
func controlParentDescriptor(sd *windows.SECURITY_DESCRIPTOR, sid *windows.SID, fixedRoot bool) error {
	if sd == nil {
		return errors.New("cannot verify Control parent")
	}
	owner, _, e := sd.Owner()
	if e != nil || !controlParentOwnerAllowed(owner, sid, fixedRoot) {
		return errors.New("unsafe Control parent owner")
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil {
		return errors.New("unsafe Control parent permissions")
	}
	// Rights that can replace a child, rewrite the ACL or change ownership.
	const modify = uint32(0x2 | 0x4 | 0x10 | 0x40 | 0x100 | 0x10000 | 0x40000 | 0x80000 | 0x10000000 | 0x40000000)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(acl, i, &ace); e != nil || ace == nil {
			return errors.New("cannot verify Control parent")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported Control parent permissions")
		}
		who := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		mask := uint32(ace.Mask)
		if !controlTrustedParent(who, sid) && !controlRootCreateACE(who, ace.Header.AceFlags, mask, fixedRoot) && mask&modify != 0 {
			return errors.New("unsafe Control parent permissions")
		}
	}
	return nil
}
func controlPrepareDir(path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	parent := filepath.Dir(abs)
	sa, sid, e := controlSecurity()
	if e != nil {
		return e
	}
	volume := filepath.VolumeName(parent)
	if volume == "" {
		return errors.New("invalid Control parent")
	}
	cur := volume + string(os.PathSeparator)
	if e = controlParent(cur, sid); e != nil {
		return e
	}
	rel, e := filepath.Rel(cur, parent)
	if e != nil {
		return e
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if _, e = os.Lstat(cur); errors.Is(e, os.ErrNotExist) {
			if e = os.Mkdir(cur, 0700); e != nil && !errors.Is(e, os.ErrExist) {
				return errors.New("cannot create Control parent")
			}
		}
		if e = controlParent(cur, sid); e != nil {
			return e
		}
	}
	p, e := windows.UTF16PtrFromString(abs)
	if e != nil {
		return e
	}
	e = windows.CreateDirectory(p, sa)
	if e != nil && !errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		return errors.New("cannot create Control directory")
	}
	f, e := controlWindowsOpen(abs, windows.READ_CONTROL, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if e != nil {
		return e
	}
	return f.Close()
}

func controlLock(path string) (func(), error) {
	f, e := controlWindowsOpen(path, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.OPEN_ALWAYS, 0)
	if e != nil {
		return nil, e
	}
	var o windows.Overlapped
	if e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &o); e != nil {
		_ = f.Close()
		return nil, e
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o); _ = f.Close() }, nil
}
func controlRead(path string) ([]byte, error) {
	f, e := controlWindowsOpen(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, 0)
	if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
		return nil, os.ErrNotExist
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return nil, errors.New("invalid Control state")
	}
	return b, nil
}

var controlReplace = func(from, to string) error {
	src, e := windows.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	dst, e := windows.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func controlAtomicWrite(path string, b []byte) error {
	if f, e := controlWindowsOpen(path, windows.READ_CONTROL, windows.OPEN_EXISTING, 0); e == nil {
		_ = f.Close()
	} else if !errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
		return e
	}
	var random [16]byte
	if _, e := rand.Read(random[:]); e != nil {
		return errors.New("cannot create Control state")
	}
	tmp := filepath.Join(filepath.Dir(path), ".control-"+hex.EncodeToString(random[:]))
	f, e := controlWindowsOpen(tmp, windows.GENERIC_WRITE|windows.READ_CONTROL, windows.CREATE_NEW, 0)
	if e != nil {
		return e
	}
	defer os.Remove(tmp)
	_, e = writeAndSync(f, b)
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return errors.New("could not save Control state")
	}
	if e = controlReplace(tmp, path); e != nil {
		return errors.New("could not save Control state")
	}
	return nil
}
