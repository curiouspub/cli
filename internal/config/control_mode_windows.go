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

// controlParent verifies that another ordinary account cannot replace a
// child directory. Privileged system principals remain part of the host's
// trust boundary, as they can change the current user's token itself.
func controlParent(path string, sid *windows.SID) error {
	fi, e := os.Lstat(path)
	if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe Control parent")
	}
	name, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return errors.New("unsafe Control parent")
	}
	attributes, e := windows.GetFileAttributes(name)
	if e != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("unsafe Control parent")
	}
	sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil || sd == nil {
		return errors.New("cannot verify Control parent")
	}
	trusted := func(s *windows.SID) bool {
		if s == nil {
			return false
		}
		return s.Equals(sid) || s.IsWellKnown(windows.WinLocalSystemSid) || s.IsWellKnown(windows.WinBuiltinAdministratorsSid)
	}
	owner, _, e := sd.Owner()
	if e != nil || !trusted(owner) {
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
		if !trusted(who) && uint32(ace.Mask)&modify != 0 {
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
