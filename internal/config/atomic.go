package config

import "io"

// writeAndSync is shared by both independently permissioned state files.
// It never reports a durable write until all bytes and the file sync succeed.
type syncWriter interface {
	Write([]byte) (int, error)
	Sync() error
}

func writeAndSync(f syncWriter, data []byte) (string, error) {
	n, e := f.Write(data)
	if e != nil {
		return "write", e
	}
	if n != len(data) {
		return "write", io.ErrShortWrite
	}
	if e = f.Sync(); e != nil {
		return "sync", e
	}
	return "", nil
}
