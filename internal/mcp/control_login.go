package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"unicode/utf8"

	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/pkg/controlwire"
)

const (
	toolControlLoginStart  = "control_login_start"
	toolControlLoginVerify = "control_login_verify"
	toolControlLogout      = "control_logout"
)

var controlPrepare = func(origin string) (flow.ControlRequest, error) {
	return flow.PrepareControlLogin(origin, "mcp", false)
}
var controlResume = func(ctx context.Context, origin, handle string) (flow.ControlRequest, error) {
	return flow.ResumeControlLogin(ctx, origin, handle, false)
}
var controlVerify = func(ctx context.Context, origin, handle string) (flow.ControlAccount, error) {
	return flow.VerifyControlLogin(ctx, origin, handle, false)
}
var controlLogout = func(ctx context.Context, origin, id string, localOnly bool) error {
	return flow.LogoutControl(ctx, origin, id, localOnly, false)
}

type controlLoginArgs struct {
	ControlURL string `json:"control_url"`
	Handle     string `json:"handle,omitempty"`
}
type controlLogoutArgs struct {
	ControlURL   string `json:"control_url"`
	CredentialID string `json:"credential_id,omitempty"`
	LocalOnly    bool   `json:"local_only,omitempty"`
}

func decodeControlArgs(raw json.RawMessage, args any) *Result {
	if !utf8.Valid(raw) {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if controlUniqueJSON(dec, 0) != nil {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	if _, e := dec.Token(); e != io.EOF {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			r := ErrorResult("The arguments could not be read.")
			return &r
		}
	}
	if controlwire.ExactFields(raw, args) != nil {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(args); e != nil {
		r := ErrorResult("The arguments could not be read.")
		return &r
	}
	return nil
}
func controlUniqueJSON(d *json.Decoder, depth int) error {
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
				return errors.New("invalid arguments")
			}
			seen[name] = true
			if e = controlUniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	case '[':
		for d.More() {
			if e = controlUniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	default:
		return errors.New("invalid arguments")
	}
}
func controlStartTool() Tool {
	return Tool{Name: toolControlLoginStart, Title: "Start a Control login", Description: "Prepare a browser login for the supplied Control origin and retain its returned handle. Call this tool again with that handle to create or resume the request, then ask the user to open the returned address and confirm the code.", InputSchema: objectSchema(`"control_url":{"type":"string","description":"The explicit HTTPS Control origin; use the same origin on both calls."},"handle":{"type":"string","description":"Omit to prepare a recovery handle; pass that returned handle to create or resume the browser request."}`, "control_url"), Handler: func(ctx context.Context, raw json.RawMessage, _ Progress) Result {
		var args controlLoginArgs
		if bad := decodeControlArgs(raw, &args); bad != nil {
			return *bad
		}
		if args.ControlURL == "" {
			return missingArgument("control_url", "the Control origin")
		}
		var result flow.ControlRequest
		var e error
		if args.Handle != "" {
			result, e = controlResume(ctx, args.ControlURL, args.Handle)
		} else {
			result, e = controlPrepare(args.ControlURL)
			if e == nil {
				result.NextTool = toolControlLoginStart
			}
		}
		if e != nil {
			var wait *flow.ControlWait
			if errors.As(e, &wait) && result.Handle != "" {
				return jsonResult(struct {
					Status     string `json:"status"`
					Handle     string `json:"handle"`
					ControlURL string `json:"control_url"`
					NextTool   string `json:"next_tool"`
					RetryAfter int    `json:"retry_after_seconds"`
				}{"waiting", result.Handle, result.ControlURL, toolControlLoginStart, int(math.Ceil(wait.After.Seconds()))})
			}
			message, resumable := flow.ControlFailure(e)
			if resumable && result.Handle != "" {
				return ErrorResult("%s Resume with request handle %s.", message, result.Handle)
			}
			return ErrorResult("%s", message)
		}
		return jsonResult(result)
	}}
}
func controlVerifyTool() Tool {
	return Tool{Name: toolControlLoginVerify, Title: "Check a Control login", Description: "Poll a previously started Control login after the user approves it in their browser. This call cannot approve a request. A recovered stored result reports the earlier save and does not revalidate the credential.", InputSchema: objectSchema(`"control_url":{"type":"string","description":"The same HTTPS Control origin used to start."},"handle":{"type":"string","description":"The recovery handle returned by the start tool."}`, "control_url", "handle"), Handler: func(ctx context.Context, raw json.RawMessage, _ Progress) Result {
		var args controlLoginArgs
		if bad := decodeControlArgs(raw, &args); bad != nil {
			return *bad
		}
		if args.ControlURL == "" {
			return missingArgument("control_url", "the Control origin")
		}
		if args.Handle == "" {
			return missingArgument("handle", "the request handle")
		}
		account, e := controlVerify(ctx, args.ControlURL, args.Handle)
		if errors.Is(e, flow.ErrControlRequestNotStarted) {
			return ErrorResult("Call control_login_start with the same origin and handle before checking approval.")
		}
		if e != nil {
			var wait *flow.ControlWait
			if errors.As(e, &wait) {
				return jsonResult(struct {
					Status     string `json:"status"`
					RetryAfter int    `json:"retry_after_seconds"`
				}{"pending", int(math.Ceil(wait.After.Seconds()))})
			}
			message, resumable := flow.ControlFailure(e)
			if resumable {
				return ErrorResult("%s Resume with the same request handle.", message)
			}
			return ErrorResult("%s", message)
		}
		return jsonResult(account)
	}}
}
func controlLogoutTool() Tool {
	return Tool{Name: toolControlLogout, Title: "Log out of Control", Description: "Revoke a selected Control credential. Local-only removal leaves remote access potentially active.", InputSchema: objectSchema(`"control_url":{"type":"string","description":"The HTTPS Control origin that issued the credential."},"credential_id":{"type":"string","description":"Select the credential when more than one is stored."},"local_only":{"type":"boolean","description":"Remove local state without claiming remote revocation."}`, "control_url"), Handler: func(ctx context.Context, raw json.RawMessage, _ Progress) Result {
		var args controlLogoutArgs
		if bad := decodeControlArgs(raw, &args); bad != nil {
			return *bad
		}
		if args.ControlURL == "" {
			return missingArgument("control_url", "the Control origin")
		}
		if e := controlLogout(ctx, args.ControlURL, args.CredentialID, args.LocalOnly); e != nil {
			return ErrorResult("The Control logout did not complete. Select the credential by its returned ID, or retry when the service is available. Local-only removal leaves remote access potentially active.")
		}
		if args.LocalOnly {
			return TextResult("Local credential removed; remote access may remain active.")
		}
		return TextResult("Credential revoked and removed.")
	}}
}
