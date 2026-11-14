package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os/exec"
	"runtime"
	"time"

	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
)

const controlLoginUsage ui.Prose = `usage: curious login --control-url <origin> [--resume <handle>]

Start or resume a browser login for the specified Control origin.
`
const controlLogoutUsage ui.Prose = `usage: curious logout --control-url <origin> [--credential-id <id>] [--local-only]

Revoke a stored Control credential. Local-only removes it here without revoking it remotely.
`

var controlBrowserOpen = openControlBrowser
var controlPrepare = func(origin string) (flow.ControlRequest, error) {
	return flow.PrepareControlLogin(origin, "cli", false)
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

func openControlBrowser(address string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
func runControlLogin(args []string, u *ui.UI, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	origin := fs.String("control-url", "", "")
	handle := fs.String("resume", "", "")
	if code, done := parseSubcommand(fs, args, 0, controlLoginUsage, u); done {
		return code
	}
	if *origin == "" {
		u.Step("A Control origin is required.\n")
		return 2
	}
	ctx := context.Background()
	var req flow.ControlRequest
	var err error
	if *handle != "" {
		req, err = controlResume(ctx, *origin, *handle)
	} else {
		req, err = controlPrepare(*origin)
		if err == nil {
			message := fmt.Sprintf("If interrupted, resume with request handle %s.\n", ui.Sanitize(req.Handle))
			n, writeErr := io.WriteString(stderr, message)
			if writeErr != nil || n != len(message) {
				return 1
			}
			req, err = controlResume(ctx, *origin, req.Handle)
		}
	}
	if err != nil {
		var wait *flow.ControlWait
		if errors.As(err, &wait) && req.Handle != "" {
			u.Step("Wait %d seconds, then resume with request handle %s.", int(math.Ceil(wait.After.Seconds())), req.Handle)
			return 1
		}
		message, resumable := flow.ControlFailure(err)
		if resumable && req.Handle != "" {
			u.Step("%s Resume with request handle %s.", message, req.Handle)
		} else {
			u.Step("%s", message)
		}
		return 1
	}
	if req.Completed {
		u.Result("Stored login for %s (credential %s).", req.UserID, req.CredentialID)
		return 0
	}
	u.Step("Open %s and enter code %s.\n", req.VerificationURI, req.UserCode)
	if err = controlBrowserOpen(req.VerificationURI); err != nil {
		u.Step("The browser could not be opened; use the address and code above.\n")
	}
	for {
		account, e := controlVerify(ctx, *origin, req.Handle)
		if e == nil {
			u.Result("Signed in as %s (credential %s).", account.UserID, account.CredentialID)
			return 0
		}
		var wait *flow.ControlWait
		if errors.As(e, &wait) {
			delay := wait.After
			if delay <= 0 {
				delay = time.Second
			}
			time.Sleep(delay)
			continue
		}
		message, resumable := flow.ControlFailure(e)
		if resumable {
			u.Step("%s Resume with request handle %s.", message, req.Handle)
		} else {
			u.Step("%s", message)
		}
		return 1
	}
}
func runControlLogout(args []string, u *ui.UI) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	origin := fs.String("control-url", "", "")
	id := fs.String("credential-id", "", "")
	localOnly := fs.Bool("local-only", false, "")
	if code, done := parseSubcommand(fs, args, 0, controlLogoutUsage, u); done {
		return code
	}
	if *origin == "" {
		u.Step("A Control origin is required.\n")
		return 2
	}
	if e := controlLogout(context.Background(), *origin, *id, *localOnly); e != nil {
		u.Step("Logout did not complete: %s\n", e.Error())
		return 1
	}
	if *localOnly {
		u.Result("Local credential removed; remote access may remain active.")
	} else {
		u.Result("Credential revoked and removed.")
	}
	return 0
}
