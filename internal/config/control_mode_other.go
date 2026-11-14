//go:build !windows

package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func controlCheck(fi os.FileInfo, dir bool) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() != dir || (!dir && !fi.Mode().IsRegular()) {
		return errors.New("unsafe Control state path")
	}
	if !dir && st.Nlink != 1 {
		return errors.New("unsafe Control state link count")
	}
	if st.Uid != uint32(os.Geteuid()) && st.Uid != 0 {
		return errors.New("unsafe Control state owner")
	}
	if fi.Mode().Perm()&0022 != 0 && !(fi.IsDir() && st.Uid == 0 && fi.Mode()&os.ModeSticky != 0) {
		return errors.New("unsafe Control state permissions")
	}
	return nil
}
func controlPrepareDir(path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	parts := strings.Split(strings.TrimPrefix(abs, string(os.PathSeparator)), string(os.PathSeparator))
	cur := string(os.PathSeparator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, e := os.Lstat(cur)
		if errors.Is(e, os.ErrNotExist) {
			mkdirErr := os.Mkdir(cur, 0700)
			if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return errors.New("cannot create Control directory")
			}
			if mkdirErr == nil {
				if e = os.Chmod(cur, 0700); e != nil {
					return errors.New("cannot restrict Control directory")
				}
			}
			fi, e = os.Lstat(cur)
		}
		if e != nil {
			return errors.New("cannot inspect Control directory")
		}
		if e = controlCheck(fi, true); e != nil {
			return e
		}
	}
	fi, e := os.Lstat(abs)
	if e != nil {
		return e
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || fi.Mode().Perm()&0077 != 0 {
		return errors.New("Control directory must be private")
	}
	return nil
}
func controlOpen(path string, flags int) (*os.File, error) {
	openFlags := flags | unix.O_CLOEXEC | unix.O_NOFOLLOW
	created := false
	var fd int
	var e error
	if flags&unix.O_CREAT != 0 {
		fd, e = unix.Open(path, openFlags|unix.O_EXCL, 0600)
		if errors.Is(e, unix.EEXIST) {
			fd, e = unix.Open(path, openFlags&^unix.O_CREAT, 0600)
		} else if e == nil {
			created = true
		}
	} else {
		fd, e = unix.Open(path, openFlags, 0600)
	}
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	if created {
		if e = f.Chmod(0600); e != nil {
			_ = f.Close()
			return nil, errors.New("cannot restrict Control file")
		}
	}
	fi, e := f.Stat()
	if e == nil {
		e = controlCheck(fi, false)
	}
	if e == nil {
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uint32(os.Geteuid()) || fi.Mode().Perm()&0077 != 0 {
			e = errors.New("Control file must be private")
		}
	}
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	return f, nil
}
func controlLock(path string) (func(), error) {
	f, e := controlOpen(path, unix.O_CREAT|unix.O_RDWR)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(f.Fd()), unix.LOCK_EX); e != nil {
		_ = f.Close()
		return nil, e
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
func controlRead(path string) ([]byte, error) {
	f, e := controlOpen(path, unix.O_RDONLY)
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

var controlReplace = os.Rename

func controlAtomicWrite(path string, b []byte) error {
	if fi, e := os.Lstat(path); e == nil {
		if e = controlCheck(fi, false); e != nil {
			return e
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uint32(os.Geteuid()) || fi.Mode().Perm()&0077 != 0 {
			return errors.New("Control file must be private")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".control-tmp-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = writeAndSync(f, b)
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return errors.New("could not save Control state")
	}
	if e = controlReplace(tmp, path); e != nil {
		return errors.New("could not save Control state")
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return errors.New("could not sync Control directory")
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return errors.New("could not sync Control directory")
	}
	return nil
}
