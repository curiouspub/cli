package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/controlwire"
)

// ControlPending is a recoverable request; Proof must never be rendered.
type ControlPending struct {
	Handle          string    `json:"handle"`
	Kind            string    `json:"kind"`
	Proof           ui.Secret `json:"-"`
	RequestID       string    `json:"request_id"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       string    `json:"expires_at"`
	NextCreateAt    string    `json:"next_create_at"`
	NextPollAt      string    `json:"next_poll_at"`
	Unresolved      bool      `json:"unresolved,omitempty"`
}
type ControlCredential struct {
	Token         ui.Secret `json:"-"`
	CredentialID  string    `json:"credential_id"`
	RequestHandle string    `json:"request_handle"`
	RequestID     string    `json:"request_id"`
	UserID        string    `json:"user_id"`
	ExpiresAt     string    `json:"expires_at"`
}
type ControlState struct {
	Pending     map[string]ControlPending
	Credentials map[string]ControlCredential
	Completed   map[string]string
}
type controlDiskPending struct {
	Handle          string `json:"handle"`
	Kind            string `json:"kind"`
	Proof           string `json:"proof"`
	RequestID       string `json:"request_id"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresAt       string `json:"expires_at"`
	NextCreateAt    string `json:"next_create_at"`
	NextPollAt      string `json:"next_poll_at"`
	Unresolved      bool   `json:"unresolved,omitempty"`
}
type controlDiskCredential struct {
	Token         string `json:"token"`
	CredentialID  string `json:"credential_id"`
	RequestHandle string `json:"request_handle"`
	RequestID     string `json:"request_id"`
	UserID        string `json:"user_id"`
	ExpiresAt     string `json:"expires_at"`
}
type controlDiskState struct {
	Origin      string                           `json:"origin"`
	Pending     map[string]controlDiskPending    `json:"pending"`
	Credentials map[string]controlDiskCredential `json:"credentials"`
	Completed   map[string]string                `json:"completed"`
}

// ControlWithLock serializes a complete state transaction across processes.
// The callback must save explicitly before it reports a durable effect.
func ControlWithLock(origin string, fn func(*ControlStore) error) error {
	path, e := Path()
	if e != nil {
		return e
	}
	root, e := filepath.Abs(filepath.Join(filepath.Dir(path), "control"))
	if e != nil {
		return errors.New("cannot resolve Control state directory")
	}
	if e = controlPrepareDir(root); e != nil {
		return e
	}
	sum := sha256.Sum256([]byte(origin))
	name := hex.EncodeToString(sum[:])
	lock, e := controlLock(filepath.Join(root, name+".lock"))
	if e != nil {
		return e
	}
	defer lock()
	return fn(&ControlStore{origin: origin, path: filepath.Join(root, name+".json")})
}

type ControlStore struct {
	origin string
	path   string
}

func (s *ControlStore) Load() (ControlState, error) {
	b, e := controlRead(s.path)
	if errors.Is(e, os.ErrNotExist) {
		return ControlState{Pending: map[string]ControlPending{}, Credentials: map[string]ControlCredential{}, Completed: map[string]string{}}, nil
	}
	if e != nil {
		return ControlState{}, e
	}
	var d controlDiskState
	if controlDecode(b, &d) != nil || d.Origin != s.origin || d.Pending == nil || d.Credentials == nil || d.Completed == nil {
		return ControlState{}, errors.New("invalid Control state")
	}
	out := ControlState{Pending: make(map[string]ControlPending, len(d.Pending)), Credentials: make(map[string]ControlCredential, len(d.Credentials)), Completed: make(map[string]string, len(d.Completed))}
	for k, p := range d.Pending {
		if k == "" || p.Handle != k || p.Proof == "" {
			return ControlState{}, errors.New("invalid Control state")
		}
		out.Pending[k] = ControlPending{Handle: p.Handle, Kind: p.Kind, Proof: ui.Secret(p.Proof), RequestID: p.RequestID, UserCode: p.UserCode, VerificationURI: p.VerificationURI, ExpiresAt: p.ExpiresAt, NextCreateAt: p.NextCreateAt, NextPollAt: p.NextPollAt, Unresolved: p.Unresolved}
	}
	for k, c := range d.Credentials {
		if c.Token == "" || c.CredentialID == "" || c.CredentialID != k || c.UserID == "" {
			return ControlState{}, errors.New("invalid Control state")
		}
		out.Credentials[k] = ControlCredential{Token: ui.Secret(c.Token), CredentialID: c.CredentialID, RequestHandle: c.RequestHandle, RequestID: c.RequestID, UserID: c.UserID, ExpiresAt: c.ExpiresAt}
	}
	for handle, id := range d.Completed {
		if handle == "" || id == "" {
			return ControlState{}, errors.New("invalid Control state")
		}
		credential, ok := out.Credentials[id]
		if !ok || credential.RequestHandle != handle {
			return ControlState{}, errors.New("invalid Control state")
		}
		if _, ok := out.Pending[handle]; ok {
			return ControlState{}, errors.New("invalid Control state")
		}
		out.Completed[handle] = id
	}
	return out, nil
}
func (s *ControlStore) Save(state ControlState) error {
	d := controlDiskState{Origin: s.origin, Pending: make(map[string]controlDiskPending, len(state.Pending)), Credentials: make(map[string]controlDiskCredential, len(state.Credentials)), Completed: make(map[string]string, len(state.Completed))}
	for k, p := range state.Pending {
		if k == "" || p.Handle != k || p.Proof == "" {
			return errors.New("invalid Control state")
		}
		d.Pending[k] = controlDiskPending{Handle: p.Handle, Kind: p.Kind, Proof: string(p.Proof), RequestID: p.RequestID, UserCode: p.UserCode, VerificationURI: p.VerificationURI, ExpiresAt: p.ExpiresAt, NextCreateAt: p.NextCreateAt, NextPollAt: p.NextPollAt, Unresolved: p.Unresolved}
	}
	for k, c := range state.Credentials {
		if c.Token == "" || c.CredentialID == "" || c.CredentialID != k || c.UserID == "" {
			return errors.New("invalid Control state")
		}
		d.Credentials[k] = controlDiskCredential{Token: string(c.Token), CredentialID: c.CredentialID, RequestHandle: c.RequestHandle, RequestID: c.RequestID, UserID: c.UserID, ExpiresAt: c.ExpiresAt}
	}
	for handle, id := range state.Completed {
		if handle == "" || id == "" {
			return errors.New("invalid Control state")
		}
		credential, ok := state.Credentials[id]
		if !ok || credential.RequestHandle != handle {
			return errors.New("invalid Control state")
		}
		if _, ok := state.Pending[handle]; ok {
			return errors.New("invalid Control state")
		}
		d.Completed[handle] = id
	}
	b, e := json.Marshal(d)
	if e != nil {
		return errors.New("could not encode Control state")
	}
	if len(b) > 1<<20 {
		return errors.New("Control state is too large")
	}
	return controlAtomicWrite(s.path, b)
}

// State is untrusted input even in a private directory: reject ambiguous
// objects instead of choosing whichever duplicate field was read last.
func controlDecode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid Control state")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if e := controlUniqueValue(d, 0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("invalid Control state")
	}
	if e := controlwire.ExactFields(data, dst); e != nil {
		return e
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return errors.New("invalid Control state")
	}
	return nil
}
func controlUniqueValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting limit exceeded")
	}
	v, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := v.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("invalid Control state")
			}
			seen[name] = true
			if e = controlUniqueValue(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	case '[':
		for d.More() {
			if e = controlUniqueValue(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	default:
		return errors.New("invalid Control state")
	}
}
