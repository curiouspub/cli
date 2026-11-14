//go:build windows

package config

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/curiouspub/cli/internal/ui"
	"golang.org/x/sys/windows"
)

func TestControlCredentialStoragePermissions(t *testing.T) {
	configPath := controlTestPath(t)
	t.Setenv("CURIOUS_CONFIG", configPath)
	if e := ControlWithLock("https://example.test", func(s *ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Pending["one"] = ControlPending{Handle: "one", Kind: "cli", Proof: ui.Secret("synthetic-proof")}
		return s.Save(st)
	}); e != nil {
		logControlParentDiagnostics(t, filepath.Dir(configPath))
		t.Fatalf("Control state setup failed: %s", diagnosticControlFailure(e))
	}
	root := filepath.Join(filepath.Dir(configPath), "control")
	entries, e := os.ReadDir(root)
	if e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}
	var statePath string
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if e != nil {
			t.Fatalf("operation failed: %s", diagnosticErr(e))
		}
		flags, _, e := sd.Control()
		if e != nil || flags&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("missing protected DACL")
		}
		acl, _, e := sd.DACL()
		if e != nil || acl == nil || acl.AceCount != 1 {
			t.Fatal("unexpected DACL")
		}
		if strings.HasSuffix(path, ".json") {
			statePath = path
		}
	}
	if statePath == "" {
		t.Fatal("missing state file")
	}
	linked := filepath.Join(root, "linked-state")
	if e = os.Link(statePath, linked); e != nil {
		t.Fatalf("cannot establish hard-link fixture: %s", diagnosticErr(e))
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); e == nil {
		t.Fatal("accepted multiply linked state")
	}
	if e = os.Remove(linked); e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}

	bad, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}
	acl, _, e := bad.DACL()
	if e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}
	if e = windows.SetNamedSecurityInfo(statePath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}
	before, e := os.ReadFile(statePath)
	if e != nil {
		t.Fatalf("operation failed: %s", diagnosticErr(e))
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); e == nil {
		t.Fatal("accepted broad file DACL")
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error {
		return s.Save(ControlState{Pending: map[string]ControlPending{"two": {Handle: "two", Kind: "cli", Proof: ui.Secret("different-synthetic-proof")}}, Credentials: map[string]ControlCredential{}})
	}); e == nil {
		t.Fatal("wrote to insecure state")
	}
	after, e := os.ReadFile(statePath)
	if e != nil || string(after) != string(before) {
		t.Fatal("insecure state changed")
	}
	t.Run("reparse parent rejected", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		link := filepath.Join(dir, "link")
		if e := os.Mkdir(target, 0700); e != nil {
			t.Fatalf("operation failed: %s", diagnosticErr(e))
		}
		if e := controlPrepareDir(filepath.Join(target, "positive")); e != nil {
			t.Fatalf("ordinary parent failed: %s", diagnosticControlFailure(e))
		}
		if e := os.Symlink(target, link); e != nil {
			t.Fatalf("cannot establish reparse-point fixture: %s", diagnosticErr(e))
		}
		if e := controlPrepareDir(filepath.Join(link, "through-link")); e == nil {
			t.Fatal("accepted reparse parent")
		}
		if _, e := os.Stat(filepath.Join(target, "through-link")); !os.IsNotExist(e) {
			t.Fatal("created state through reparse point")
		}
	})
}

// These diagnostics describe the existing parent checks without changing them.
// Only fixed classifications, numeric metadata and one-way SID digests are
// emitted; paths, account names and security descriptor contents stay local.
func diagnosticErr(err error) string {
	if err == nil {
		return "none"
	}
	var code syscall.Errno
	if errors.As(err, &code) {
		return fmt.Sprintf("errno:%d", uint32(code))
	}
	return "non-system-error"
}
func diagnosticControlFailure(err error) string {
	if err == nil {
		return "none"
	}
	switch err.Error() {
	case "unsafe Control parent owner":
		return "parent-owner"
	case "cannot verify Control volume root":
		return "volume-root-unverified"
	case "unsafe Control parent":
		return "parent-path"
	case "cannot verify Control parent":
		return "parent-security-query"
	case "unsafe Control parent permissions":
		return "parent-permissions"
	case "cannot verify Control parent permissions":
		return "parent-acl-query"
	case "unsupported Control parent permissions":
		return "parent-ace-unsupported"
	default:
		return diagnosticErr(err)
	}
}

// diagnosticWithin validates a byte range without overflow.
func diagnosticWithin(total, offset, size uint64) bool {
	return offset <= total && size <= total-offset
}

// Standard allow and deny ACE types are the only layouts decoded here.
func diagnosticKnownACE(kind byte) bool { return kind == 0 || kind == 1 }

// A SID has an eight-byte header followed by up to 15 four-byte subauthorities.
func diagnosticSIDFits(aceSize, sidOffset uint64, count byte) bool {
	return count <= 15 && diagnosticWithin(aceSize, sidOffset, 8+4*uint64(count))
}
func diagnosticSID(sid, current *windows.SID) string {
	if sid == nil {
		return "nil"
	}
	if !sid.IsValid() {
		return "invalid"
	}
	switch {
	case current != nil && sid.Equals(current):
		return "current-user"
	case sid.IsWellKnown(windows.WinLocalSystemSid):
		return "local-system"
	case sid.IsWellKnown(windows.WinBuiltinAdministratorsSid):
		return "builtin-administrators"
	default:
		digest := sha256.Sum256([]byte(sid.String()))
		return fmt.Sprintf("unrecognized-sha256:%x", digest)
	}
}
func logControlDescriptor(t *testing.T, label string, sd *windows.SECURITY_DESCRIPTOR, current *windows.SID) {
	t.Helper()
	if sd == nil {
		t.Logf("diagnostic %s descriptor=nil", label)
		return
	}
	owner, defaulted, ownerErr := sd.Owner()
	t.Logf("diagnostic %s owner=%s owner-error=%s owner-defaulted=%t", label, diagnosticSID(owner, current), diagnosticErr(ownerErr), defaulted)
	control, _, controlErr := sd.Control()
	acl, aclDefaulted, aclErr := sd.DACL()
	t.Logf("diagnostic %s control=%#x control-error=%s dacl-nil=%t dacl-error=%s dacl-defaulted=%t", label, uint16(control), diagnosticErr(controlErr), acl == nil, diagnosticErr(aclErr), aclDefaulted)
	if aclErr != nil || acl == nil {
		return
	}
	// GetSecurityInfo provides a native ACL header. The unexported size field
	// is read from the standard little-endian ACL header before inspecting ACEs.
	aclHeaderSize := uint64(unsafe.Sizeof(*acl))
	aclHeader := unsafe.Slice((*byte)(unsafe.Pointer(acl)), int(aclHeaderSize))
	aclSize := uint64(binary.LittleEndian.Uint16(aclHeader[2:4]))
	t.Logf("diagnostic %s ace-count=%d acl-size=%d", label, acl.AceCount, aclSize)
	if aclSize < aclHeaderSize {
		t.Logf("diagnostic %s malformed-acl-size", label)
		return
	}
	base := uintptr(unsafe.Pointer(acl))
	count := int(acl.AceCount)
	if count > 128 {
		count = 128
		t.Logf("diagnostic %s ace-list-truncated", label)
	}
	for i := 0; i < count; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		err := windows.GetAce(acl, uint32(i), &ace)
		if err != nil || ace == nil {
			t.Logf("diagnostic %s ace=%d error=%s nil=%t", label, i, diagnosticErr(err), ace == nil)
			continue
		}
		address := uintptr(unsafe.Pointer(ace))
		if address < base || uint64(address-base) < aclHeaderSize || !diagnosticWithin(aclSize, uint64(address-base), uint64(unsafe.Sizeof(windows.ACE_HEADER{}))) {
			t.Logf("diagnostic %s ace=%d malformed-header-range", label, i)
			continue
		}
		offset := uint64(address - base)
		aceSize := uint64(ace.Header.AceSize)
		if aceSize < uint64(unsafe.Sizeof(windows.ACE_HEADER{})) || !diagnosticWithin(aclSize, offset, aceSize) {
			t.Logf("diagnostic %s ace=%d malformed-entry-range", label, i)
			continue
		}
		kind := ace.Header.AceType
		principal := "unsupported-or-short"
		var mask uint32
		sidOffset := uint64(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart))
		if diagnosticKnownACE(kind) {
			if diagnosticWithin(aceSize, sidOffset, 8) {
				header := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(ace), uintptr(sidOffset))), 8)
				if diagnosticSIDFits(aceSize, sidOffset, header[1]) {
					mask = uint32(ace.Mask)
					principal = diagnosticSID((*windows.SID)(unsafe.Pointer(&ace.SidStart)), current)
				} else {
					principal = "malformed-sid-range"
				}
			}
		}
		t.Logf("diagnostic %s ace=%d type=%d flags=%#x size=%d mask=%#x principal=%s", label, i, kind, ace.Header.AceFlags, ace.Header.AceSize, mask, principal)
	}
}
func logControlParentDiagnostics(t *testing.T, parent string) {
	t.Helper()
	sa, current, identityErr := controlSecurity()
	t.Logf("diagnostic identity-error=%s current-nil=%t", diagnosticErr(identityErr), current == nil)
	if identityErr != nil || current == nil {
		return
	}

	// This is only a descriptor/API positive control in the synthetic test
	// directory; it does not call or bypass the production parent check.
	probe := filepath.Join(parent, "descriptor-positive-control")
	native, nameErr := windows.UTF16PtrFromString(probe)
	if nameErr != nil {
		t.Logf("diagnostic positive-name-error=%s", diagnosticErr(nameErr))
	} else {
		createErr := windows.CreateDirectory(native, sa)
		t.Logf("diagnostic positive-create-error=%s", diagnosticErr(createErr))
		if createErr == nil {
			defer os.Remove(probe)
			sd, err := windows.GetNamedSecurityInfo(probe, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			t.Logf("diagnostic positive-named-error=%s descriptor-nil=%t", diagnosticErr(err), sd == nil)
			if err == nil {
				logControlDescriptor(t, "positive-named", sd, current)
			}
			file, err := controlWindowsOpen(probe, windows.READ_CONTROL, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS)
			t.Logf("diagnostic positive-handle-verification-error=%s file-nil=%t", diagnosticErr(err), file == nil)
			if file != nil {
				_ = file.Close()
			}
		}
	}

	abs, err := filepath.Abs(parent)
	if err != nil {
		t.Logf("diagnostic absolute-path-error=%s", diagnosticErr(err))
		return
	}
	volume := filepath.VolumeName(abs)
	if volume == "" {
		t.Log("diagnostic missing-volume")
		return
	}
	cur := volume + string(os.PathSeparator)
	rel, err := filepath.Rel(cur, abs)
	if err != nil {
		t.Logf("diagnostic relative-path-error=%s", diagnosticErr(err))
		return
	}
	paths := []string{cur}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		paths = append(paths, cur)
	}
	for index, path := range paths {
		kind := "ancestor"
		if index == 0 {
			kind = "volume-root"
		}
		if index == len(paths)-1 {
			kind = "synthetic-parent"
		}
		info, statErr := os.Lstat(path)
		dir, symlink := false, false
		if info != nil {
			dir = info.IsDir()
			symlink = info.Mode()&os.ModeSymlink != 0
		}
		name, err := windows.UTF16PtrFromString(path)
		var attrs uint32
		attrErr := err
		if attrErr == nil {
			attrs, attrErr = windows.GetFileAttributes(name)
		}
		t.Logf("diagnostic depth=%d class=%s lstat-error=%s directory=%t symlink=%t attr-error=%s reparse=%t", index, kind, diagnosticErr(statErr), dir, symlink, diagnosticErr(attrErr), attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0)
		sd, namedErr := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		t.Logf("diagnostic depth=%d named-error=%s descriptor-nil=%t", index, diagnosticErr(namedErr), sd == nil)
		if namedErr == nil {
			logControlDescriptor(t, "named", sd, current)
		}
		verdict := controlParent(path, current)
		t.Logf("diagnostic depth=%d production-parent-accepted=%t production-result=%s", index, verdict == nil, diagnosticControlFailure(verdict))
		if verdict == nil {
			continue
		}
		// Compare the same rejected object via a read-only handle, without
		// changing ownership, DACLs, privileges, or the selected test root.
		if nameErr := error(nil); name != nil {
			h, openErr := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			nameErr = openErr
			t.Logf("diagnostic rejected-handle-open-error=%s", diagnosticErr(nameErr))
			if openErr == nil {
				handleSD, handleErr := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
				t.Logf("diagnostic rejected-handle-error=%s descriptor-nil=%t", diagnosticErr(handleErr), handleSD == nil)
				if handleErr == nil {
					logControlDescriptor(t, "rejected-handle", handleSD, current)
				}
				_ = windows.CloseHandle(h)
			}
		} else {
			t.Log("diagnostic rejected-handle-name-unavailable")
		}
		return
	}
	t.Log("diagnostic no-parent-rejected-on-readback")
}

func TestControlWindowsRootIdentity(t *testing.T) {
	const volume = `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\`
	for _, tc := range []struct {
		name, path string
		want       bool
	}{
		{"ordinary root", `C:\`, true}, {"lowercase root", `d:\`, true}, {"directory", `C:\child`, false},
		{"relative", `C:`, false}, {"unc", `\\server\share\`, false}, {"device", `\\.\C:\`, false}, {"extended", `\\?\C:\`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if controlDriveRoot(tc.path) != tc.want {
				t.Fatal("wrong drive-root classification")
			}
		})
	}
	for _, tc := range []struct {
		name, actual, expected string
		drive, fixed           uint32
		want                   bool
	}{
		{"native root", volume, volume, windows.DRIVE_FIXED, windows.DRIVE_FIXED, true},
		{"case insensitive", strings.ToUpper(volume), volume, windows.DRIVE_FIXED, windows.DRIVE_FIXED, true},
		{"directory alias", volume + `child`, volume, windows.DRIVE_FIXED, windows.DRIVE_FIXED, false},
		{"different volume", volume, strings.Replace(volume, "01234567", "11234567", 1), windows.DRIVE_FIXED, windows.DRIVE_FIXED, false},
		{"mapped network", volume, volume, windows.DRIVE_REMOTE, windows.DRIVE_FIXED, false},
		{"unknown drive", volume, volume, windows.DRIVE_UNKNOWN, windows.DRIVE_FIXED, false},
		{"unknown volume", volume, volume, windows.DRIVE_FIXED, windows.DRIVE_UNKNOWN, false},
		{"malformed guid", strings.Replace(volume, "-", "x", 1), volume, windows.DRIVE_FIXED, windows.DRIVE_FIXED, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if controlRootNamesAgree(tc.actual, tc.expected, tc.drive, tc.fixed) != tc.want {
				t.Fatal("wrong volume identity decision")
			}
		})
	}
	for _, tc := range []struct {
		name, drive, volume, handle string
		want                        bool
	}{
		{"same native device root", `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\`, true},
		{"case insensitive device", `\device\harddiskvolume1`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\`, true},
		{"subst to directory", `\??\C:\child`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\child`, false},
		{"subst to root", `\??\C:\`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\`, false},
		{"different device", `\Device\HarddiskVolume2`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\`, false},
		{"different handle object", `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1`, `\Device\HarddiskVolume1\child`, false},
		{"missing device", "", "", `\`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if controlDeviceNamesAgree(tc.drive, tc.volume, tc.handle) != tc.want {
				t.Fatal("wrong device mapping decision")
			}
		})
	}
	root := filepath.VolumeName(t.TempDir()) + `\`
	if !controlDriveRoot(root) {
		t.Fatal("test root is not an ordinary drive root")
	}
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal("invalid native root")
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("cannot open native root: %s", diagnosticErr(err))
	}
	defer windows.CloseHandle(h)
	if !controlNativeFixedRoot(root, h) {
		t.Fatal("actual local fixed root was not proven")
	}
	if controlNativeFixedRoot(root, windows.InvalidHandle) {
		t.Fatal("invalid handle was accepted")
	}
	child := t.TempDir()
	childName, err := windows.UTF16PtrFromString(child)
	if err != nil {
		t.Fatal("invalid child")
	}
	childHandle, err := windows.CreateFile(childName, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("cannot open child: %s", diagnosticErr(err))
	}
	defer windows.CloseHandle(childHandle)
	if controlNativeFixedRoot(root, childHandle) {
		t.Fatal("directory alias or mismatched handle was accepted as root")
	}
	if controlNativeFixedRoot(`\\server\share\`, h) || controlNativeFixedRoot(`\\?\C:\`, h) || controlNativeFixedRoot(`\\.\C:\`, h) {
		t.Fatal("nonordinary path was accepted")
	}
}

// controlTestUnusedDOSDevice preserves the native absence result privately;
// the production helper intentionally redacts all lookup errors.
func controlTestUnusedDOSDevice(name string) (bool, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	var target [1024]uint16
	_, err = windows.QueryDosDevice(ptr, &target[0], uint32(len(target)))
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return true, nil
	}
	return false, err
}

// TestControlWindowsDeviceAliases uses only unused drive letters in the
// current logon namespace. It does not create or write through the aliases.
func TestControlWindowsDeviceAliases(t *testing.T) {
	processUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || processUser.User.Sid == nil || processUser.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("cannot establish a non-system process token")
	}
	effectiveUser, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil || effectiveUser.User.Sid == nil || !effectiveUser.User.Sid.Equals(processUser.User.Sid) {
		t.Fatal("cannot establish the current logon token")
	}
	dir := t.TempDir()
	root := filepath.VolumeName(dir) + `\`
	for _, tc := range []struct{ name, path string }{{"volume root", root}, {"directory", dir}} {
		t.Run(tc.name, func(t *testing.T) {
			drives, err := windows.GetLogicalDrives()
			if err != nil {
				t.Fatal("cannot inspect drive availability")
			}
			var letter string
			for i := 25; i >= 2; i-- {
				if drives&(1<<uint(i)) != 0 {
					continue
				}
				candidate := string(rune('A'+i)) + ":"
				unused, err := controlTestUnusedDOSDevice(candidate)
				if err != nil {
					t.Fatal("cannot establish unused drive mapping")
				}
				if unused {
					letter = candidate
					break
				}
			}
			if letter == "" {
				t.Fatal("no unused drive letter available")
			}
			device, err := windows.UTF16PtrFromString(letter)
			if err != nil {
				t.Fatal("invalid fixture device")
			}
			targetText := `\??\` + tc.path
			target, err := windows.UTF16PtrFromString(targetText)
			if err != nil {
				t.Fatal("invalid fixture target")
			}
			// Recheck both views immediately before adding the local definition.
			drives, err = windows.GetLogicalDrives()
			if err != nil || drives&(1<<uint(letter[0]-'A')) != 0 {
				t.Fatal("drive no longer unused")
			}
			unused, err := controlTestUnusedDOSDevice(letter)
			if err != nil || !unused {
				t.Fatal("mapping no longer unused")
			}
			const create = windows.DDD_RAW_TARGET_PATH | windows.DDD_NO_BROADCAST_SYSTEM
			if err = windows.DefineDosDevice(create, device, target); err != nil {
				t.Fatal("cannot create local fixture mapping")
			}
			t.Cleanup(func() {
				actual, err := controlDOSDevice(letter)
				if err != nil || !strings.EqualFold(actual, targetText) {
					t.Error("fixture mapping changed before cleanup")
				}
				const remove = windows.DDD_REMOVE_DEFINITION | windows.DDD_EXACT_MATCH_ON_REMOVE | windows.DDD_RAW_TARGET_PATH | windows.DDD_NO_BROADCAST_SYSTEM
				if err = windows.DefineDosDevice(remove, device, target); err != nil {
					t.Error("cannot remove exact fixture mapping")
				}
				var mappings [1024]uint16
				n, queryErr := windows.QueryDosDevice(device, &mappings[0], uint32(len(mappings)))
				if errors.Is(queryErr, windows.ERROR_FILE_NOT_FOUND) {
					return
				}
				if queryErr != nil || n == 0 || n >= uint32(len(mappings)) {
					t.Error("cannot verify fixture cleanup")
					return
				}
				for offset := 0; offset < int(n) && mappings[offset] != 0; {
					end := offset
					for end < int(n) && mappings[end] != 0 {
						end++
					}
					if strings.EqualFold(windows.UTF16ToString(mappings[offset:end]), targetText) {
						t.Error("fixture mapping remains after cleanup")
					}
					offset = end + 1
				}
			})
			actual, err := controlDOSDevice(letter)
			if err != nil || !strings.EqualFold(actual, targetText) {
				t.Fatal("fixture mapping did not match target")
			}
			alias := letter + `\`
			name, err := windows.UTF16PtrFromString(alias)
			if err != nil {
				t.Fatal("invalid fixture alias")
			}
			h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err != nil {
				t.Fatal("cannot open fixture alias")
			}
			defer windows.CloseHandle(h)
			if controlNativeFixedRoot(alias, h) {
				t.Fatal("local alias received root exception")
			}
		})
	}
}

func TestControlWindowsRootPolicy(t *testing.T) {
	_, current, err := controlSecurity()
	if err != nil {
		t.Fatal("cannot identify current user")
	}
	service, err := controlInstallerSID()
	if err != nil {
		t.Fatal("cannot construct service SID")
	}
	var wrong *windows.SID
	err = windows.AllocateAndInitializeSid(&windows.SECURITY_NT_AUTHORITY, 6, 80, 956008885, 3418522649, 1831038044, 1853292631, 2271478465, 0, 0, &wrong)
	if err != nil || wrong == nil {
		t.Fatal("cannot construct wrong SID")
	}
	defer windows.FreeSid(wrong)
	if service.IdentifierAuthority() != windows.SECURITY_NT_AUTHORITY || service.SubAuthorityCount() != 6 {
		t.Fatal("wrong service SID authority")
	}
	for i, want := range [...]uint32{80, 956008885, 3418522649, 1831038044, 1853292631, 2271478464} {
		if service.SubAuthority(uint32(i)) != want {
			t.Fatal("wrong service SID subauthority")
		}
	}
	auth, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		t.Fatal("cannot construct authenticated-users SID")
	}
	other, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal("cannot construct other SID")
	}
	if !controlParentOwnerAllowed(service, current, true) || controlParentOwnerAllowed(service, current, false) || controlParentOwnerAllowed(wrong, current, true) || controlTrustedParent(service, current) {
		t.Fatal("root-only owner distinction failed")
	}
	if !controlRootCreateACE(auth, 0, 0x4, true) {
		t.Fatal("exact root create-only ACE was refused")
	}
	for _, tc := range []struct {
		name  string
		who   *windows.SID
		flags byte
		mask  uint32
		root  bool
	}{
		{"other principal", other, 0, 0x4, true}, {"nonroot", auth, 0, 0x4, false}, {"flags", auth, 1, 0x4, true}, {"inherit only", auth, 8, 0x4, true},
		{"extra create", auth, 0, 0x6, true}, {"delete child", auth, 0, 0x40, true}, {"delete", auth, 0, 0x10000, true},
		{"change acl", auth, 0, 0x40000, true}, {"take ownership", auth, 0, 0x80000, true}, {"generic all", auth, 0, 0x10000000, true}, {"generic write", auth, 0, 0x40000000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if controlRootCreateACE(tc.who, tc.flags, tc.mask, tc.root) {
				t.Fatal("broader ACE exception accepted")
			}
		})
	}
	// Exercise the production descriptor validator using native descriptors,
	// with the current user retained as owner and a full-access principal.
	for _, tc := range []struct {
		name, owner, ace, want string
		root                   bool
	}{
		{"ordinary secure nonroot", current.String(), "", "", false},
		{"ordinary secure root", current.String(), "", "", true},
		{"exact root", current.String(), "(A;;0x4;;;AU)", "", true},
		{"same nonroot", current.String(), "(A;;0x4;;;AU)", "unsafe Control parent permissions", false},
		{"other principal", current.String(), "(A;;0x4;;;WD)", "unsafe Control parent permissions", true},
		{"service writer", current.String(), "(A;;0x4;;;" + service.String() + ")", "unsafe Control parent permissions", true},
		{"flags", current.String(), "(A;OI;0x4;;;AU)", "unsafe Control parent permissions", true},
		{"extra create", current.String(), "(A;;0x6;;;AU)", "unsafe Control parent permissions", true},
		{"delete child", current.String(), "(A;;0x40;;;AU)", "unsafe Control parent permissions", true},
		{"delete", current.String(), "(A;;0x10000;;;AU)", "unsafe Control parent permissions", true},
		{"change acl", current.String(), "(A;;0x40000;;;AU)", "unsafe Control parent permissions", true},
		{"take ownership", current.String(), "(A;;0x80000;;;AU)", "unsafe Control parent permissions", true},
		{"generic all", current.String(), "(A;;0x10000000;;;AU)", "unsafe Control parent permissions", true},
		{"generic write", current.String(), "(A;;0x40000000;;;AU)", "unsafe Control parent permissions", true},
		{"service owner root", service.String(), "", "", true},
		{"service owner nonroot", service.String(), "", "unsafe Control parent owner", false},
		{"wrong service owner", wrong.String(), "", "unsafe Control parent owner", true},
	} {
		t.Run("descriptor/"+tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString("O:" + tc.owner + "D:P(A;;FA;;;" + current.String() + ")" + tc.ace)
			if err != nil {
				t.Fatal("cannot construct descriptor")
			}
			err = controlParentDescriptor(sd, current, tc.root)
			if tc.want == "" && err != nil {
				t.Fatal("expected descriptor to pass")
			}
			if tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatal("descriptor did not reach expected refusal")
			}
		})
	}
	// An untrusted create-child ACE in a newly created ordinary parent is
	// never covered by the root exception, even when the owner is current.
	sa, _, err := controlSecurity()
	if err != nil {
		t.Fatal("cannot construct descriptor")
	}
	path := filepath.Join(t.TempDir(), "competing-child")
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal("invalid test child")
	}
	if err = windows.CreateDirectory(ptr, sa); err != nil {
		t.Fatalf("create test child: %s", diagnosticErr(err))
	}
	if err = controlParent(path, current); err != nil {
		t.Fatalf("secure ordinary child refused: %s", diagnosticControlFailure(err))
	}
	bad, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + current.String() + ")(A;;0x4;;;AU)")
	if err != nil {
		t.Fatal("cannot construct test ACL")
	}
	acl, _, err := bad.DACL()
	if err != nil {
		t.Fatal("cannot read test ACL")
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatalf("set test ACL: %s", diagnosticErr(err))
	}
	if err = controlParent(path, current); err == nil || err.Error() != "unsafe Control parent permissions" {
		t.Fatal("untrusted ordinary child did not reach DACL refusal")
	}
}
