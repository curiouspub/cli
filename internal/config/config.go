package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
)

// SchemaVersion is the version this build writes into a config file it
// creates. It exists from the first write rather than being added later:
// a format that goes into the field unversioned can never be told apart
// from the next one, and the migration has to guess.
const SchemaVersion = 1

const (
	// envConfigPath names a config FILE outright, overriding every other
	// rule below. It is the escape hatch, and it is also what makes this
	// package's own tests hermetic — see the package doc.
	envConfigPath = "CURIOUS_CONFIG"

	// envXDGConfigHome is the base-directory standard's own variable. A
	// user who has moved their whole config tree has said where it goes,
	// and a tool that ignores it puts a file somewhere they will not
	// think to look.
	envXDGConfigHome = "XDG_CONFIG_HOME"

	// dirName is this tool's directory inside whichever config root wins,
	// and fileName the file inside it.
	dirName  = "curious"
	fileName = "config.json"
)

// Field names on disk. They are written out once, here, so the reader
// and the writer cannot drift apart by a typo — the merge in Save writes
// through the same three keys Load strips.
const (
	keyVersion = "version"
	keyToken   = "token"
	keyAPIURL  = "api_url"
)

// fileConfig is the ON-DISK shape, and its Token is a plain string ON
// PURPOSE. This is the single most dangerous line in the package, so it
// gets the longest comment:
//
// ui.Secret redacts itself through every marshalling path it can reach —
// JSON, text, binary — which is exactly what makes it wrong here. Marshal
// a struct holding one and the file receives the placeholder instead of
// the token. The write succeeds, the file is valid JSON, correctly
// permissioned and atomically renamed, and every later run loads the
// placeholder, sends it as a bearer token, takes an authentication
// failure and logs in again. The symptom is "login never sticks", which
// shows up nowhere near this line and points at the server.
//
// So the conversion happens at this boundary and nowhere else: Secret
// covers the value everywhere in memory, and the string it wraps is
// written to disk by the one piece of code whose whole job is writing it
// to disk. That is what Secret's own documentation says the escape hatch
// is for.
type fileConfig struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
	APIURL  string `json:"api_url"`
}

// Config is the configuration as this run should understand it: the
// values from the file, plus what Load found out while reading it.
//
// The report fields sit here rather than in a second return value
// because every one of them has to survive the same journey — a caller
// that has a Config has everything it needs to explain itself to the
// user, and cannot lose half of it by discarding a value.
type Config struct {
	// Version is the schema version read from the file, or SchemaVersion
	// when there was no file. A version this build does not recognise is
	// carried through unchanged rather than rewritten: downgrading a
	// newer file's version number is a lie about what is in it.
	Version int

	// Token is the bearer token, and it is EMPTY whenever there is not a
	// usable one — no file, an unreadable file, a corrupt file, or a
	// token issued against a different endpoint. A caller decides what to
	// do about that by looking at this field, never by inspecting an
	// error string.
	//
	// It is exported, and that is required rather than stylistic: fmt
	// cannot call a method on a value it reaches by reflecting an
	// unexported field, so a ui.Secret hidden below one prints in full.
	Token ui.Secret

	// APIURL is the endpoint recorded in the file — the one the stored
	// token was issued against. It is reported even when the token is
	// being ignored, because "issued against somewhere else" is only a
	// useful thing to say if you can say where.
	APIURL string

	// Path is the file this Config was read from, and the file Save
	// writes to. Every message this package produces names it: a user
	// told to delete their config cannot act on that without knowing
	// which file it is.
	Path string

	// Warnings are things worth telling the user that do not stop the
	// run. A caller prints them and carries on.
	Warnings []string

	// NoTokenReason explains an empty Token when the reason is worth
	// repeating to the user — a corrupt file, an unreadable one, a token
	// belonging to another endpoint. It is nil on a first run, where
	// there is nothing to explain, and nil when Token is set.
	//
	// It is deliberately NOT the error Load returns. A caller writing the
	// ordinary `if err != nil { return err }` would then abort a
	// perfectly recoverable run — the whole point of these cases is that
	// the flow continues into a login.
	NoTokenReason error

	// PermissionsChecked reports whether this platform could check who
	// else can read the token file. It is false on Windows, where the
	// mode bits do not carry that meaning and this package deliberately
	// makes no claim about them.
	//
	// It exists so that "no warning" cannot be mistaken for "checked and
	// safe". A caller that wants to say something about file protection
	// has to look at this first, and silence about an unchecked file is
	// then a decision rather than an accident.
	PermissionsChecked bool

	// unknown holds every top-level field this build does not know,
	// exactly as it was read, so Save can write it back. Losing it would
	// mean an older binary run once silently deletes a newer one's state.
	unknown map[string]json.RawMessage
}

// Path returns the config file's location, resolving in this order:
//
//  1. CURIOUS_CONFIG, used verbatim as the path to the FILE.
//  2. XDG_CONFIG_HOME, joined with this tool's directory and file name.
//  3. The platform default — see defaultConfigDir.
//
// A relative XDG_CONFIG_HOME is ignored, which the base-directory
// standard requires and which is also just safer: resolved against the
// working directory it would scatter a token file into whatever
// directory the user happened to be standing in.
func Path() (string, error) {
	if p := os.Getenv(envConfigPath); p != "" {
		return p, nil
	}
	if base := os.Getenv(envXDGConfigHome); filepath.IsAbs(base) {
		return filepath.Join(base, dirName, fileName), nil
	}
	dir, err := defaultConfigDir()
	if err != nil {
		return "", fmt.Errorf(
			"could not work out where to keep the config file: %w — set %s to a "+
				"full path and the command will use that instead", err, envConfigPath)
	}
	return filepath.Join(dir, dirName, fileName), nil
}

// Load reads the config file and reports what it found.
//
// effectiveAPIURL is the endpoint this run will actually talk to, and it
// is required: the stored token is only offered back when it was issued
// against that same endpoint. Passing an empty string is a wiring
// mistake rather than a user error, and it is refused loudly here rather
// than silently turning into "no token", which would cost a login on
// every single run and look like a server problem.
//
// The returned error is reserved for a failure that leaves this package
// with nothing to say — today, only being unable to work out where the
// file lives. Everything else that can go wrong with a config file is
// reported through the Config: a corrupt file, an unreadable one, or a
// token belonging elsewhere all come back as an empty Token and a
// NoTokenReason, because every one of them is recoverable by logging in
// again and none of them should end the run.
//
// Load never writes to the file, and that includes never repairing it.
func Load(effectiveAPIURL string) (*Config, error) {
	if effectiveAPIURL == "" {
		return nil, errors.New(
			"config.Load needs the API endpoint this run is talking to, so a stored " +
				"token issued against a different one is not reused; pass the same " +
				"base URL the client was built with")
	}

	path, err := Path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Version: SchemaVersion, Path: path}

	if fi, statErr := os.Stat(path); statErr == nil {
		warning, checked := modeWarning(path, fi)
		cfg.PermissionsChecked = checked
		if warning != "" {
			cfg.Warnings = append(cfg.Warnings, warning)
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		// No file is the ordinary first run, not a problem: no warning,
		// no reason, nothing for the caller to print.
		return cfg, nil
	}
	if err != nil {
		cfg.NoTokenReason = fmt.Errorf(
			"could not read the config file at %s: %w — fix the file's permissions, "+
				"or delete it and run this command again to log in from scratch",
			path, err)
		return cfg, nil
	}

	// Decoded TWICE, into the struct and into a map, and both results are
	// kept. The struct is what this build understands; the map is
	// everything the file actually contains, which is what makes writing
	// back a field this build has never heard of possible.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		cfg.NoTokenReason = corruptError(path, err)
		return cfg, nil
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		cfg.NoTokenReason = corruptError(path, err)
		return cfg, nil
	}

	if fc.Version != 0 {
		cfg.Version = fc.Version
	}
	cfg.APIURL = fc.APIURL
	for _, known := range []string{keyVersion, keyToken, keyAPIURL} {
		delete(raw, known)
	}
	cfg.unknown = raw

	if strings.TrimSpace(fc.Token) == "" {
		cfg.NoTokenReason = fmt.Errorf(
			"the config file at %s has no token in it — delete the file and run this "+
				"command again to log in", path)
		return cfg, nil
	}

	if reason := endpointMismatch(fc.APIURL, effectiveAPIURL); reason != nil {
		cfg.NoTokenReason = reason
		return cfg, nil
	}

	cfg.Token = ui.Secret(fc.Token)
	return cfg, nil
}

// corruptError is the one message a user gets for a file this package
// cannot parse. It names the path, says plainly what is wrong, and gives
// an action — deleting the file is safe advice precisely because nothing
// here will do it for them.
func corruptError(path string, err error) error {
	return fmt.Errorf(
		"the config file at %s looks corrupted and could not be read: %w — delete "+
			"the file and run this command again to log in from scratch", path, err)
}

// endpointMismatch reports why a stored token must not be used against
// the endpoint in force, or nil when it may be.
//
// The comparison is on api.CanonicalKey rather than on the raw strings,
// because the raw strings compare unequal for spellings that name one
// server: a trailing slash, a differently-cased host, a port written out
// that the scheme implies anyway. Left as a byte comparison, adding one
// slash to an environment variable logs the user out. This calls the
// canonical form the client package already publishes instead of growing
// a second one here — two normalisers drift, and the drift shows up as
// "login never sticks" rather than as a diff.
//
// CanonicalKey's result is used for COMPARISON ONLY and never as a base
// URL. It carries an explicit port the user may never have typed, and
// what gets dialled is the client's own base.
//
// An error from CanonicalKey is a MISMATCH, not a failure. A stored URL
// that cannot be canonicalised — truncated, carrying credentials, or
// naming a scheme this client does not speak — is unusable as evidence
// that the token belongs here, and unusable evidence is the same as
// negative evidence. It costs a login, where the other direction spends
// a real token against a server it was never issued for.
func endpointMismatch(stored, effective string) error {
	storedKey, err := api.CanonicalKey(stored)
	if err != nil {
		if stored == "" {
			return errors.New(
				"the config file does not record which server its token was issued " +
					"against, so the token is not being reused — you will be asked to " +
					"log in again")
		}
		return fmt.Errorf(
			"the config file records %q as the server its token was issued against, "+
				"and that cannot be read as an address (%w), so the token is not being "+
				"reused — you will be asked to log in again", stored, err)
	}

	effectiveKey, err := api.CanonicalKey(effective)
	if err != nil {
		return fmt.Errorf(
			"this run is talking to %q, which cannot be read as an address (%w), so "+
				"the stored token is not being reused — you will be asked to log in "+
				"again", effective, err)
	}

	if storedKey != effectiveKey {
		return fmt.Errorf(
			"the stored token was issued against %s and this run is talking to %s, "+
				"so it is not being reused — you will be asked to log in again",
			stored, effective)
	}
	return nil
}

// renameFile is os.Rename, reached through a variable so the failure that
// makes the atomic write worth having can actually be exercised. A crash
// or an error between "the new file exists" and "the new file is the
// config" is the whole reason for the temp-file-and-rename dance, and a
// claim about it that no test can reach is a claim nobody has checked.
var renameFile = os.Rename

// Save writes c to its Path atomically, with the file mode this token
// deserves.
//
// The shape, and why each step is there:
//
//   - The directory is created 0700, and chmod'ed to 0700 AFTERWARDS if
//     we were the ones who created it. mkdir's mode argument is masked
//     by the process umask exactly as open's is, so a restrictive umask
//     can produce a directory we cannot even write into. A directory
//     that was already there is left alone: it may be one the user
//     pointed us at, and re-permissioning somebody else's directory is
//     not this tool's business.
//
//   - The token is written to a temp file in the SAME directory. A temp
//     file somewhere else cannot be renamed into place — rename across
//     filesystems fails — and the fallback everyone reaches for then is
//     a copy, which is exactly the non-atomic write this avoids.
//
//   - The mode is set explicitly BEFORE any bytes are written, so the
//     token never exists on disk in a file anyone else could read, not
//     even for the length of one write call.
//
//   - Sync before rename. Rename is atomic with respect to the
//     directory, but the file's own contents are not on the disk until
//     they are flushed; without this a crash can leave the config
//     pointing at an empty file, which reads as "logged out".
//
//   - Rename over the target, never a truncate-in-place. The usual
//     argument is a crash between truncating and writing, which leaves
//     an empty config that reads as "logged out" and costs the user
//     another login. That argument is true and it is the weaker one.
//
//     THE STRONGER ONE IS MEASURED. Put the config in a directory the
//     process cannot write to — a read-only parent, a mount gone
//     read-only, a sandbox — and the two implementations diverge in the
//     worst possible direction. This one refuses: creating the temp file
//     fails with a permission error, nothing is touched, and the
//     existing credential is exactly where it was. A truncate-in-place
//     SUCCEEDS, because the permission that governs writing to a file
//     that already exists is the FILE's, not the directory's — so it
//     empties the only copy of the token and then fails on the write it
//     could not do anyway.
//
//     So the naive form destroys the credential in precisely the case
//     where this form declines to proceed, and it does it without a
//     crash, a signal or any timing at all. Reproduced directly: 26
//     bytes of real config to 0, in a directory chmod'ed 0500, while
//     the atomic path returned "permission denied" and changed nothing.
func (c *Config) Save() error {
	if c.Token == "" {
		return errors.New(
			"refusing to write a config file with no token in it: the file exists to " +
				"hold one, and a token-less file is exactly what Load reports as " +
				"corrupt on the next run")
	}

	if c.Path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		c.Path = p
	}

	dir := filepath.Dir(c.Path)
	_, statErr := os.Stat(dir)
	weCreatedIt := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create the config directory %s: %w", dir, err)
	}
	if weCreatedIt {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf(
				"could not restrict the config directory %s to its owner: %w", dir, err)
		}
	}

	data, err := c.marshal()
	if err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("could not create a temporary file in %s: %w", dir, err)
	}
	tmp := f.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = f.Close()
			// A half-written token left lying beside the real config is
			// both a leak and litter. Removing it is best effort: there
			// is nothing useful to tell the user if even this fails.
			_ = os.Remove(tmp)
		}
	}()

	// Explicit, and not redundant beside the mode the temp file was
	// created with: a create mode is masked by the process umask, so it
	// is a ceiling rather than a setting. This is the line that makes the
	// mode exactly 0600 whatever the umask is — in both directions.
	if err := chmodFile(f); err != nil {
		return fmt.Errorf("could not restrict %s to its owner: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("could not write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("could not flush %s to disk: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("could not close %s: %w", tmp, err)
	}
	if err := renameFile(tmp, c.Path); err != nil {
		return fmt.Errorf(
			"could not move the new config into place at %s: %w — the existing "+
				"config, if there was one, is untouched", c.Path, err)
	}
	renamed = true
	return nil
}

// marshal renders the file's bytes: every field this build does not know
// about, with the three it does written over the top.
//
// The merge is done as a map rather than by marshalling a struct, and
// that is not a style choice. It is the ONLY place unknown fields can be
// preserved, and it is also the exact spot where marshalling a struct
// that held a ui.Secret would put the redaction placeholder into the
// file. Both reasons point at the same code; see fileConfig.
func (c *Config) marshal() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(c.unknown)+3)
	for k, v := range c.unknown {
		out[k] = v
	}

	version := c.Version
	if version == 0 {
		version = SchemaVersion
	}
	for key, value := range map[string]any{
		keyVersion: version,
		// string(c.Token) is the boundary conversion, and the only one in
		// this package. Anything else here writes "[redacted]" into the
		// file and produces a login that never sticks.
		keyToken:  string(c.Token),
		keyAPIURL: c.APIURL,
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encoding the %s field: %w", key, err)
		}
		out[key] = encoded
	}

	// Indented, because a human being will open this file — to check
	// which server it points at, or to delete it after this package told
	// them to. Map keys marshal in sorted order, so two saves of the same
	// values produce the same bytes.
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the config file: %w", err)
	}
	return append(data, '\n'), nil
}
