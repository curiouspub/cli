package flow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/ui"
)

// ControlRequest contains only information safe to display to the user.
type ControlRequest struct {
	Handle          string `json:"handle"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	ExpiresAt       string `json:"expires_at"`
	Prepared        bool   `json:"prepared,omitempty"`
	ControlURL      string `json:"control_url,omitempty"`
	NextTool        string `json:"next_tool,omitempty"`
	Completed       bool   `json:"completed,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	CredentialID    string `json:"credential_id,omitempty"`
}
type ControlAccount struct {
	CredentialID         string `json:"credential_id"`
	UserID               string `json:"user_id"`
	ExpiresAt            string `json:"expires_at"`
	RecoveredFromStorage bool   `json:"recovered_from_storage,omitempty"`
}

// ControlFailure describes only fixed, nonsecret outcomes and whether the
// same handle may be resumed. Terminal outcomes never suggest a retry.
func ControlFailure(err error) (string, bool) {
	if errors.Is(err, errControlUncertain) {
		return "The login outcome is uncertain and cannot safely be resumed before its deadline.", false
	}
	if errors.Is(err, errControlExpired) {
		return "The login request expired. Start a new login if needed.", false
	}
	if errors.Is(err, errControlCredentialExpired) {
		return "The stored credential expired. Start a new login if needed.", false
	}
	var problem *api.ControlError
	if errors.As(err, &problem) {
		switch problem.Code {
		case "access_denied":
			return "The login request was denied. Start a new login if needed.", false
		case "request_expired":
			return "The login request expired. Start a new login if needed.", false
		case "restart_required":
			return "The earlier credential was revoked. Start a new login if needed.", false
		case "invalid_credential":
			return "The login request or credential was refused.", false
		case "request_conflict":
			return "The login request conflicts with its stored identity.", false
		case "invalid_request":
			return "The Control service refused the login request as invalid.", false
		case "authority_unavailable":
			return "The Control service is temporarily unavailable.", true
		}
	}
	return "The Control login could not be completed.", true
}

var errControlUncertain = errors.New("login outcome uncertain")
var errControlExpired = errors.New("login request expired")
var errControlCredentialExpired = errors.New("stored credential expired")

// ErrControlRequestNotStarted means the prepared handle needs its start call.
var ErrControlRequestNotStarted = errors.New("resume the login request before polling")

type ControlWait struct{ After time.Duration }

func (w *ControlWait) Error() string { return "authorization is pending" }

func newControl(origin string, allowLoopback bool) (string, *api.ControlClient, error) {
	canonical, e := api.ControlOrigin(origin, allowLoopback)
	if e != nil {
		return "", nil, e
	}
	c, e := api.NewControlClient(canonical, allowLoopback)
	return canonical, c, e
}

// Account labels are untrusted response data. Never render one that embeds
// any proof or credential currently held by this origin.
func safeControlUserID(value string, state config.ControlState, incoming ui.Secret) bool {
	if incoming != "" && strings.Contains(value, string(incoming)) {
		return false
	}
	for _, pending := range state.Pending {
		if pending.Proof != "" && strings.Contains(value, string(pending.Proof)) {
			return false
		}
	}
	for _, credential := range state.Credentials {
		if credential.Token != "" && strings.Contains(value, string(credential.Token)) {
			return false
		}
	}
	return true
}

func validControlHandle(value string) bool {
	b, e := base64.RawURLEncoding.DecodeString(value)
	return e == nil && len(b) == 16 && base64.RawURLEncoding.EncodeToString(b) == value
}
func randomControlBytes(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", errors.New("could not create login request")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PrepareControlLogin durably saves a fresh proof and returns a nonsecret
// recovery handle before any creation request can be sent.
func PrepareControlLogin(origin, kind string, allowLoopback bool) (ControlRequest, error) {
	var out ControlRequest
	canonical, _, e := newControl(origin, allowLoopback)
	if e != nil {
		return out, e
	}
	if kind != "cli" && kind != "mcp" {
		return out, errors.New("invalid client kind")
	}
	proof, e := randomControlBytes(32)
	if e != nil {
		return out, e
	}
	handle, e := randomControlBytes(16)
	if e != nil {
		return out, e
	}
	e = config.ControlWithLock(canonical, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if _, exists := st.Pending[handle]; exists {
			return errors.New("could not create unique login request")
		}
		if _, exists := st.Completed[handle]; exists {
			return errors.New("could not create unique login request")
		}
		for _, existing := range st.Pending {
			if existing.Proof == ui.Secret(proof) {
				return errors.New("could not create unique login request")
			}
		}
		p := config.ControlPending{Handle: handle, Kind: kind, Proof: ui.Secret(proof), ExpiresAt: time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)}
		st.Pending[handle] = p
		if e = s.Save(st); e != nil {
			return e
		}
		out = ControlRequest{Handle: handle, ExpiresAt: p.ExpiresAt, Prepared: true, ControlURL: canonical}
		return nil
	})
	return out, e
}

// recoverUnresolved conservatively bounds an operation whose outcome was not
// durably recorded before the previous holder released the origin lock.
func recoverUnresolved(s *config.ControlStore, st config.ControlState, p *config.ControlPending) error {
	if !p.Unresolved {
		return nil
	}
	deadline, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return errors.New("invalid stored login request")
	}
	next := time.Now().UTC().Add(600 * time.Second)
	prior := p.NextPollAt
	if p.RequestID == "" {
		prior = p.NextCreateAt
	}
	if prior != "" {
		known, parseErr := time.Parse(time.RFC3339Nano, prior)
		if parseErr != nil {
			return errors.New("invalid stored login request")
		}
		if known.After(next) {
			next = known
		}
	}
	if !next.Before(deadline) {
		return errControlUncertain
	}
	if p.RequestID == "" {
		p.NextCreateAt = next.Format(time.RFC3339Nano)
	} else {
		p.NextPollAt = next.Format(time.RFC3339Nano)
	}
	p.Unresolved = false
	st.Pending[p.Handle] = *p
	if err = s.Save(st); err != nil {
		return err
	}
	return &ControlWait{After: time.Until(next)}
}

func controlCreate(ctx context.Context, c *api.ControlClient, s *config.ControlStore, st config.ControlState, p config.ControlPending) (ControlRequest, error) {
	out := ControlRequest{Handle: p.Handle, ExpiresAt: p.ExpiresAt}
	if e := recoverUnresolved(s, st, &p); e != nil {
		return out, e
	}
	deadline, e := time.Parse(time.RFC3339, p.ExpiresAt)
	if e != nil || !time.Now().Before(deadline) {
		return out, errControlExpired
	}
	if p.NextCreateAt != "" {
		next, e := time.Parse(time.RFC3339Nano, p.NextCreateAt)
		if e != nil {
			return out, errors.New("invalid stored login request")
		}
		if wait := time.Until(next); wait > 0 {
			return out, &ControlWait{After: wait}
		}
	}
	if e = c.Capabilities(ctx); e != nil {
		return out, e
	}
	raw, e := base64.RawURLEncoding.DecodeString(string(p.Proof))
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != string(p.Proof) {
		return out, errors.New("invalid stored login request")
	}
	sum := sha256.Sum256(raw)
	p.Unresolved = true
	st.Pending[p.Handle] = p
	if e = s.Save(st); e != nil {
		return out, e
	}
	response, e := c.Create(ctx, base64.RawURLEncoding.EncodeToString(sum[:]), p.Kind)
	if e != nil {
		var problem *api.ControlError
		delay := 5 * time.Second
		if errors.As(e, &problem) && problem.Code == "slow_down" && problem.RetryAfter > delay {
			delay = problem.RetryAfter
		}
		p.NextCreateAt = time.Now().UTC().Add(delay).Format(time.RFC3339Nano)
		p.Unresolved = false
		st.Pending[p.Handle] = p
		if saveErr := s.Save(st); saveErr != nil {
			return out, errControlUncertain
		}
		if errors.As(e, &problem) && problem.Code == "slow_down" {
			return out, &ControlWait{After: delay}
		}
		return out, e
	}

	for handle, other := range st.Pending {
		if handle == p.Handle || other.RequestID == "" {
			continue
		}
		otherDeadline, err := time.Parse(time.RFC3339, other.ExpiresAt)
		if err != nil {
			return out, errors.New("invalid stored login request")
		}
		if other.RequestID == response.RequestID || (time.Now().Before(otherDeadline) && other.UserCode == response.UserCode) {
			return out, errors.New("login request identity conflict")
		}
	}
	for _, credential := range st.Credentials {
		if credential.RequestID != "" && credential.RequestID == response.RequestID && credential.RequestHandle != p.Handle {
			return out, errors.New("login request identity conflict")
		}
	}
	serverDeadline, _ := time.Parse(time.RFC3339, response.ExpiresAt)
	if serverDeadline.Before(deadline) {
		p.ExpiresAt = response.ExpiresAt
	}
	p.NextCreateAt = ""
	p.Unresolved = false
	p.RequestID = response.RequestID
	p.UserCode = response.UserCode
	p.VerificationURI = response.VerificationURIComplete
	if p.NextPollAt == "" {
		p.NextPollAt = time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339Nano)
	}
	st.Pending[p.Handle] = p
	if e = s.Save(st); e != nil {
		return out, errControlUncertain
	}
	return ControlRequest{Handle: p.Handle, UserCode: p.UserCode, VerificationURI: p.VerificationURI, ExpiresAt: p.ExpiresAt}, nil
}

// ResumeControlLogin repeats creation using the stored proof when needed.
func ResumeControlLogin(ctx context.Context, origin, handle string, allowLoopback bool) (ControlRequest, error) {
	var out ControlRequest
	canonical, c, e := newControl(origin, allowLoopback)
	if e != nil {
		return out, e
	}
	if !validControlHandle(handle) {
		return out, errors.New("a valid request handle is required")
	}
	e = config.ControlWithLock(canonical, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if id, ok := st.Completed[handle]; ok {
			c := st.Credentials[id]
			expires, e := time.Parse(time.RFC3339, c.ExpiresAt)
			if e != nil || !time.Now().Before(expires) {
				return errControlCredentialExpired
			}
			if !safeControlUserID(c.UserID, st, "") {
				return errors.New("invalid stored account")
			}
			out = ControlRequest{Handle: handle, Completed: true, UserID: c.UserID, CredentialID: id, ExpiresAt: c.ExpiresAt}
			return nil
		}
		// A credential can be durable even if cleanup was interrupted.
		for _, saved := range st.Credentials {
			if saved.RequestHandle != handle {
				continue
			}
			pending, exists := st.Pending[handle]
			if !exists || pending.RequestID == "" || saved.RequestID != pending.RequestID {
				return errors.New("invalid stored account")
			}
			expires, err := time.Parse(time.RFC3339, saved.ExpiresAt)
			if err != nil || !time.Now().Before(expires) {
				return errControlCredentialExpired
			}
			if !safeControlUserID(saved.UserID, st, "") {
				return errors.New("invalid stored account")
			}
			out = ControlRequest{Handle: handle, Completed: true, UserID: saved.UserID, CredentialID: saved.CredentialID, ExpiresAt: saved.ExpiresAt}
			return nil
		}
		p, ok := st.Pending[handle]
		if !ok {
			return errors.New("login request was not found")
		}
		if p.RequestID == "" {
			out, e = controlCreate(ctx, c, s, st, p)
			return e
		}
		if e = recoverUnresolved(s, st, &p); e != nil {
			return e
		}
		deadline, err := time.Parse(time.RFC3339, p.ExpiresAt)
		if err != nil {
			return errors.New("invalid stored login request")
		}
		if !time.Now().Before(deadline) {
			return errControlExpired
		}
		if !c.ValidateVerification(p.UserCode, p.VerificationURI) {
			return errors.New("invalid stored login request")
		}
		out = ControlRequest{Handle: p.Handle, UserCode: p.UserCode, VerificationURI: p.VerificationURI, ExpiresAt: p.ExpiresAt}
		return nil
	})
	if out.Handle != "" {
		out.ControlURL = canonical
	}
	return out, e
}

// VerifyControlLogin polls one request and saves its credential before success.
func VerifyControlLogin(ctx context.Context, origin, handle string, allowLoopback bool) (ControlAccount, error) {
	var out ControlAccount
	canonical, c, e := newControl(origin, allowLoopback)
	if e != nil {
		return out, e
	}
	if !validControlHandle(handle) {
		return out, errors.New("a valid request handle is required")
	}
	e = config.ControlWithLock(canonical, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if id, ok := st.Completed[handle]; ok {
			c := st.Credentials[id]
			expires, e := time.Parse(time.RFC3339, c.ExpiresAt)
			if e != nil || !time.Now().Before(expires) {
				return errControlCredentialExpired
			}
			if !safeControlUserID(c.UserID, st, "") {
				return errors.New("invalid stored account")
			}
			out = ControlAccount{CredentialID: id, UserID: c.UserID, ExpiresAt: c.ExpiresAt, RecoveredFromStorage: true}
			return nil
		}
		// A credential can be durable even if cleanup was interrupted.
		for _, saved := range st.Credentials {
			if saved.RequestHandle != handle {
				continue
			}
			pending, exists := st.Pending[handle]
			if !exists || pending.RequestID == "" || saved.RequestID != pending.RequestID {
				return errors.New("invalid stored account")
			}
			expires, err := time.Parse(time.RFC3339, saved.ExpiresAt)
			if err != nil || !time.Now().Before(expires) {
				return errControlCredentialExpired
			}
			if !safeControlUserID(saved.UserID, st, "") {
				return errors.New("invalid stored account")
			}
			out = ControlAccount{CredentialID: saved.CredentialID, UserID: saved.UserID, ExpiresAt: saved.ExpiresAt, RecoveredFromStorage: true}
			return nil
		}
		p, ok := st.Pending[handle]
		if !ok {
			return errors.New("login request was not found")
		}
		if e = recoverUnresolved(s, st, &p); e != nil {
			return e
		}
		deadline, e := time.Parse(time.RFC3339, p.ExpiresAt)
		if e != nil || !time.Now().Before(deadline) {
			return errControlExpired
		}
		if p.RequestID == "" {
			return ErrControlRequestNotStarted
		}
		raw, e := base64.RawURLEncoding.DecodeString(string(p.Proof))
		if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != string(p.Proof) || !c.ValidateVerification(p.UserCode, p.VerificationURI) {
			return errors.New("invalid stored login request")
		}
		if p.NextPollAt != "" {
			next, e := time.Parse(time.RFC3339Nano, p.NextPollAt)
			if e != nil {
				return errors.New("invalid stored login request")
			}
			if wait := time.Until(next); wait > 0 {
				return &ControlWait{After: wait}
			}
		}
		p.Unresolved = true
		st.Pending[handle] = p
		if e = s.Save(st); e != nil {
			return e
		}
		issued, e := c.Poll(ctx, p.RequestID, p.Proof)
		if e != nil {
			var ce *api.ControlError
			delay := 5 * time.Second
			if errors.As(e, &ce) && ce.Code == "slow_down" && ce.RetryAfter > delay {
				delay = ce.RetryAfter
			}
			p.NextPollAt = time.Now().UTC().Add(delay).Format(time.RFC3339Nano)
			p.Unresolved = false
			st.Pending[handle] = p
			if se := s.Save(st); se != nil {
				return errControlUncertain
			}
			if errors.As(e, &ce) && (ce.Code == "authorization_pending" || ce.Code == "slow_down") {
				return &ControlWait{After: delay}
			}
			return e
		}

		if !safeControlUserID(issued.UserID, st, issued.Credential) {
			return errors.New("invalid account response")
		}
		if old, ok := st.Credentials[issued.CredentialID]; ok && (old.RequestHandle != handle || old.RequestID != p.RequestID || old.UserID != issued.UserID || old.Token != issued.Credential) {
			return errors.New("credential identity conflict")
		}
		p.Unresolved = false
		st.Pending[handle] = p
		st.Credentials[issued.CredentialID] = config.ControlCredential{Token: issued.Credential, CredentialID: issued.CredentialID, RequestHandle: handle, RequestID: p.RequestID, UserID: issued.UserID, ExpiresAt: issued.ExpiresAt}
		// Preserve the proof in the first durable write. A failure after a
		// rename but before its sync must still leave a recovery route.
		if e = s.Save(st); e != nil {
			return errControlUncertain
		}
		out = ControlAccount{CredentialID: issued.CredentialID, UserID: issued.UserID, ExpiresAt: issued.ExpiresAt}
		delete(st.Pending, handle)
		st.Completed[handle] = issued.CredentialID
		// The credential is already durable. Cleanup is best effort and
		// cannot turn a completed login into a false failure.
		_ = s.Save(st)
		return nil
	})
	return out, e
}

// LogoutControl revokes one selected credential, or explicitly deletes local state only.
func LogoutControl(ctx context.Context, origin, id string, localOnly, allowLoopback bool) error {
	canonical, c, e := newControl(origin, allowLoopback)
	if e != nil {
		return e
	}
	return config.ControlWithLock(canonical, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if id == "" {
			if len(st.Credentials) != 1 {
				return errors.New("select a credential to log out")
			}
			for k := range st.Credentials {
				id = k
			}
		}
		cred, ok := st.Credentials[id]
		if !ok {
			return errors.New("credential was not found")
		}
		if !localOnly {
			if e = c.Revoke(ctx, cred.Token); e != nil {
				return e
			}
		}
		delete(st.Credentials, id)
		for handle, credentialID := range st.Completed {
			if credentialID == id {
				delete(st.Completed, handle)
			}
		}
		return s.Save(st)
	})
}
