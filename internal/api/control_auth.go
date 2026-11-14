package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/controlwire"
)

// ControlClient speaks only to one explicitly supplied origin.
type ControlClient struct {
	origin string
	http   *http.Client
}

// ControlError is a protocol error with no server-supplied free text.
type ControlError struct {
	Code       string
	RetryAfter time.Duration
}

func (e *ControlError) Error() string { return "control login: " + e.Code }

// ControlOrigin validates a Control origin. The HTTP exception is for isolated tests only.
func ControlOrigin(raw string, allowLoopback bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", errors.New("a valid Control origin is required")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !allowLoopback || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return "", errors.New("a secure Control origin is required")
		}
	}
	if u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || strings.ContainsAny(u.Host, "\\ \t\r\n") {
		return "", errors.New("a valid Control origin is required")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "%") {
		return "", errors.New("a valid Control origin is required")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("a valid Control origin is required")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return "", errors.New("a valid Control origin is required")
				}
			}
		}
	} else {
		host = ip.String()
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("a valid Control origin is required")
		}
		if (u.Scheme == "https" && n == 443) || (u.Scheme == "http" && n == 80) {
			port = ""
		} else {
			port = strconv.Itoa(n)
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = host + ":" + port
	}
	return (&url.URL{Scheme: u.Scheme, Host: host}).String(), nil
}

func NewControlClient(origin string, allowLoopback bool) (*ControlClient, error) {
	canonical, err := ControlOrigin(origin, allowLoopback)
	if err != nil {
		return nil, err
	}
	return &ControlClient{origin: canonical, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// strictObject checks both duplicate keys and exact object schemas.
func strictObject(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid response")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueValue(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("invalid response")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("invalid response")
	}
	return nil
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting limit exceeded")
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("invalid response")
			}
			seen[s] = true
			if e = uniqueValue(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	case '[':
		for d.More() {
			if e = uniqueValue(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	default:
		return errors.New("invalid response")
	}
}
func (c *ControlClient) call(ctx context.Context, method, path string, body any, credential ui.Secret, out any, statuses ...int) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return errors.New("could not encode request")
		}
		reader = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if e != nil {
		return errors.New("could not create request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+string(credential))
	}
	res, e := c.http.Do(req)
	if e != nil {
		return errors.New("control request failed")
	}
	defer res.Body.Close()
	if len(res.Header.Values("Cache-Control")) != 1 || strings.ToLower(strings.TrimSpace(res.Header.Get("Cache-Control"))) != "no-store" {
		return errors.New("invalid control response")
	}
	if res.StatusCode != http.StatusNoContent {
		if len(res.Header.Values("Content-Type")) != 1 {
			return errors.New("invalid control response")
		}
		media, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || strings.ToLower(params["charset"]) != "utf-8" || len(params) != 1 {
			return errors.New("invalid control response")
		}
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, controlwire.MaxResponseBytes+1))
	if e != nil || len(b) > controlwire.MaxResponseBytes {
		return errors.New("invalid control response")
	}
	for _, s := range statuses {
		if res.StatusCode == s {
			if out == nil {
				if len(b) != 0 {
					return errors.New("invalid control response")
				}
				return nil
			}
			if strictObject(b, out) != nil {
				return errors.New("invalid control response")
			}
			return nil
		}
	}
	var er controlwire.ErrorResponse
	if strictObject(b, &er) != nil || er.Error == "" {
		return errors.New("invalid control response")
	}
	allowed := map[string]bool{"authorization_pending": true, "access_denied": true, "request_expired": true, "invalid_credential": true, "slow_down": true, "restart_required": true, "invalid_request": true, "request_conflict": true, "authority_unavailable": true}
	if !allowed[er.Error] {
		return errors.New("invalid control response")
	}
	expectedStatus := map[string]int{"authorization_pending": 202, "access_denied": 403, "request_expired": 410, "invalid_credential": 401, "slow_down": 429, "restart_required": 409, "invalid_request": 400, "request_conflict": 409, "authority_unavailable": 503}
	if expectedStatus[er.Error] != res.StatusCode && !(er.Error == "invalid_request" && res.StatusCode == http.StatusMethodNotAllowed) {
		return errors.New("invalid control response")
	}
	var retry time.Duration
	if er.Error == "slow_down" {
		if er.RetryAfterSeconds == nil || *er.RetryAfterSeconds < 1 || *er.RetryAfterSeconds > 600 {
			return errors.New("invalid control response")
		}
		if len(res.Header.Values("Retry-After")) != 1 || res.Header.Get("Retry-After") != strconv.Itoa(*er.RetryAfterSeconds) {
			return errors.New("invalid control response")
		}
		retry = time.Duration(*er.RetryAfterSeconds) * time.Second
	} else if er.RetryAfterSeconds != nil {
		return errors.New("invalid control response")
	}
	return &ControlError{Code: er.Error, RetryAfter: retry}
}
func (c *ControlClient) Capabilities(ctx context.Context) error {
	var v controlwire.Capabilities
	if e := c.call(ctx, http.MethodGet, controlwire.CapabilitiesPath, nil, "", &v, 200); e != nil {
		return e
	}
	expected := controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}
	if v.Version != 1 || v.IntervalSeconds != controlwire.IntervalSeconds || len(v.ClientKinds) != 2 || v.ClientKinds[0] != "cli" || v.ClientKinds[1] != "mcp" || v.Endpoints != expected {
		return errors.New("unsupported Control capabilities")
	}
	return nil
}
func controlID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	v := strings.TrimPrefix(value, prefix)
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(b) == 16 && base64.RawURLEncoding.EncodeToString(b) == v
}
func controlTime(value string) (time.Time, error) {
	t, e := time.Parse("2006-01-02T15:04:05Z", value)
	if e != nil || t.Format("2006-01-02T15:04:05Z") != value {
		return time.Time{}, errors.New("invalid timestamp")
	}
	return t, nil
}
func controlCode(value string) bool {
	if len(value) != 9 || value[4] != '-' {
		return false
	}
	for i, r := range value {
		if i == 4 {
			continue
		}
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZ23456789", r) {
			return false
		}
	}
	return true
}
func controlCredential(value, id string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "ccl1" || parts[1] != id || !controlID(id, "clcred_") {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[2])
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == parts[2]
}

// ValidateVerification refuses a stored address unless it remains bound to
// the requested origin and its one displayed code.
func (c *ControlClient) ValidateVerification(code, address string) bool {
	return controlCode(code) && address == c.origin+controlwire.VerificationPath+"?code="+url.QueryEscape(code)
}
func (c *ControlClient) Create(ctx context.Context, proofDigest, kind string) (controlwire.CreateResponse, error) {
	var v controlwire.CreateResponse
	e := c.call(ctx, http.MethodPost, controlwire.RequestsPath, controlwire.CreateRequest{ProofSHA256: proofDigest, ClientKind: kind}, "", &v, 200, 201)
	if e != nil {
		return v, e
	}
	if !controlID(v.RequestID, "clreq_") || !controlCode(v.UserCode) || v.IntervalSeconds != controlwire.IntervalSeconds {
		return v, errors.New("invalid control response")
	}
	when, e := controlTime(v.ExpiresAt)
	if e != nil || when.Location() != time.UTC || !when.After(time.Now()) {
		return v, errors.New("invalid control response")
	}
	if v.VerificationURI != c.origin+controlwire.VerificationPath || !c.ValidateVerification(v.UserCode, v.VerificationURIComplete) {
		return v, errors.New("invalid verification address")
	}
	return v, nil
}

type ControlIssuance struct {
	Credential   ui.Secret
	CredentialID string
	UserID       string
	ExpiresAt    string
}

func (c *ControlClient) Poll(ctx context.Context, id string, proof ui.Secret) (ControlIssuance, error) {
	var v controlwire.IssuanceResponse
	e := c.call(ctx, http.MethodPost, controlwire.PollPath, controlwire.PollRequest{RequestID: id, Proof: string(proof)}, "", &v, 200)
	if e != nil {
		return ControlIssuance{}, e
	}
	if !controlCredential(v.Credential, v.CredentialID) || v.UserID == "" || len(v.UserID) > 256 || !utf8.ValidString(v.UserID) {
		return ControlIssuance{}, errors.New("invalid control response")
	}
	expires, err := controlTime(v.ExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return ControlIssuance{}, errors.New("invalid control response")
	}
	return ControlIssuance{Credential: ui.Secret(v.Credential), CredentialID: v.CredentialID, UserID: v.UserID, ExpiresAt: v.ExpiresAt}, nil
}
func (c *ControlClient) Revoke(ctx context.Context, credential ui.Secret) error {
	return c.call(ctx, http.MethodPost, controlwire.RevokePath, nil, credential, nil, 204)
}
