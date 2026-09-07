package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// knownKeys is every field this build understands, in one place, so the
// reader, the writer and the ambiguity check cannot disagree about what
// "known" means.
var knownKeys = []string{keyVersion, keyToken, keyAPIURL}

// stripKnownKeys removes from raw every key this build understands, so
// what is left is exactly the fields it does not.
//
// THE COMPARISON IS THE DECODER'S OWN RELATION, NOT AN EXACT MATCH, and
// that is the whole of it. Filling the struct and stripping the map are
// two answers to one question — "is this key one this build knows?" —
// and for a while they were computed by two different relations. The
// JSON decoder matches a field name by folding case in the Unicode
// sense; strings.EqualFold is that same relation, which the row beside
// this asserts rather than assumes, so a change to either side is a
// failure somebody sees instead of a defect nobody does.
//
// What the disagreement cost: a key known to the decoder and unknown to
// the stripper was read as the token AND preserved as a field from the
// future. Written back beside the real one, it then decided every later
// run — a login writing a fresh token into a file whose other spelling
// of the same field kept answering with the old one.
func stripKnownKeys(raw map[string]json.RawMessage) {
	for key := range raw {
		for _, known := range knownKeys {
			if strings.EqualFold(key, known) {
				delete(raw, key)
				break
			}
		}
	}
}

// ambiguousSpellings reports the spellings of ONE known field that a
// file carries more than one of, or nil when it carries at most one of
// each.
//
// There is deliberately no merge and no precedence rule. Two spellings
// of one field is a file nobody can read the intent of: the format says
// they are the same field, the person who wrote them plainly meant
// something, and no rule this package could pick would be more than a
// guess about which. Picking one silently is exactly how the stale-token
// failure happened — the run kept saving a fresh token under one
// spelling and kept sending an old one from the other.
//
// The result is sorted so the report is the same on every run; a map's
// iteration order is not something a user should see change underneath
// them while they are trying to fix their file.
func ambiguousSpellings(raw map[string]json.RawMessage) []string {
	for _, known := range knownKeys {
		var found []string
		for key := range raw {
			if strings.EqualFold(key, known) {
				found = append(found, key)
			}
		}
		if len(found) > 1 {
			slices.Sort(found)
			return found
		}
	}
	return nil
}

// ambiguousError names BOTH spellings, escaped to ASCII.
//
// The escaping is the message. Two keys that fold together can render
// identically — a capital K and U+212A KELVIN SIGN are the same picture
// — so a report that printed them as they are would read as the same
// word twice and tell the user nothing they could act on. %+q is what
// makes the difference visible in a terminal.
func ambiguousError(path string, spellings []string) error {
	rendered := make([]string, len(spellings))
	for i, s := range spellings {
		rendered[i] = fmt.Sprintf("%+q", s)
	}
	return fmt.Errorf(
		"the config file at %s carries %s, which the file format treats as the same "+
			"field — so there is no way to tell which one was meant and nothing is "+
			"being read from it. Open the file and keep exactly one of them, or "+
			"delete it and run this command again to log in from scratch",
		path, strings.Join(rendered, " and "))
}

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

	// Unknown holds every top-level field this build does not know,
	// exactly as it was read, so Save can write it back. Losing it would
	// mean an older binary run once silently deletes a newer one's state.
	//
	// It is EXPORTED for the same reason Token is, and the reason is not
	// stylistic: fmt cannot call a method on a value it reaches by
	// reflecting an unexported field, so a type that redacts itself
	// stops redacting the moment it is hidden below one. This field's
	// values are raw JSON out of a file this build has never seen the
	// schema of, which is exactly where the next credential will arrive.
	// See UnknownFields.
	Unknown UnknownFields
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
		// A DIRECTORY GETS ITS OWN BRANCH, because it is the natural
		// mistake rather than an exotic one: the base-directory variable
		// takes a directory, so pointing the file override at one is
		// following the convention. Without this the read fails with the
		// operating system's own "is a directory" and the generic
		// message advises changing the permissions of the directory and
		// then deleting it — two actions, neither of them the problem.
		if fi.IsDir() {
			cfg.NoTokenReason = fmt.Errorf(
				"%s names %s, which is a directory rather than a file — it has to be "+
					"the full path to the config file itself, ending in %s. (The "+
					"base-directory variable %s is the one that takes a directory.)",
				envConfigPath, path, fileName, envXDGConfigHome)
			return cfg, nil
		}
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

	// A BYTE-ORDER MARK IS TOLERATED AT THE FRONT. The comments in this
	// package say, correctly, that a person will open this file by hand,
	// and some editors write a mark when they save a UTF-8 file. A
	// config that stops working because somebody looked at it is a
	// miserable thing to be on the receiving end of. Only at the front,
	// and only one: anything else in front of the JSON is still not
	// JSON.
	data = bytes.TrimPrefix(data, utf8BOM)

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

	// THE VERSION GATE COMES FIRST, before anything else this function
	// might complain about. A file from a release this build does not
	// know is a file whose every other property this build is not
	// entitled to have an opinion about — including how its field names
	// are spelled. It also used to be reported as corrupted, which the
	// code already knew was wrong: the map decode had succeeded and the
	// version had been read before the sentence was printed.
	if cfg.Version != SchemaVersion {
		cfg.NoTokenReason = versionError(path, cfg.Version)
		return cfg, nil
	}

	// Before anything is read OUT of the file, whether it can be read at
	// all. Nothing below this point — not the endpoint, not the
	// preserved unknown fields — is taken from a file whose own field
	// names disagree about what it says.
	if spellings := ambiguousSpellings(raw); spellings != nil {
		cfg.NoTokenReason = ambiguousError(path, spellings)
		return cfg, nil
	}

	cfg.APIURL = fc.APIURL
	stripKnownKeys(raw)
	cfg.Unknown = raw

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

// utf8BOM is the byte-order mark an editor may write at the front of a
// UTF-8 file. It carries no information here — the encoding is fixed —
// so it is trimmed on the way in and never written on the way out.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// versionError explains a schema this build does not read.
//
// Two sentences rather than one, because the two cases suggest opposite
// actions. A HIGHER version is almost certainly a newer release's file,
// and the useful advice is to upgrade — deleting it would work and would
// throw away whatever the newer release put there. Any other unknown
// value was never written by anything, so there is no newer release to
// upgrade to and the file is simply not usable.
func versionError(path string, version int) error {
	if version > SchemaVersion {
		return fmt.Errorf(
			"the config file at %s was written by a newer curious: it records schema "+
				"version %d and this build reads version %d, so nothing is being taken "+
				"from it. Upgrade curious to use the file. Deleting it and logging in "+
				"again would also work, at the cost of whatever the newer version "+
				"stored there", path, version, SchemaVersion)
	}
	return fmt.Errorf(
		"the config file at %s records schema version %d, which no release of "+
			"curious has ever written (this build reads version %d) — delete the file "+
			"and run this command again to log in from scratch",
		path, version, SchemaVersion)
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
		// %s of a redacted error rather than %w, and the trade is
		// deliberate: the wrapped error quotes the raw URL a second
		// time, which is where half of the userinfo leak lived. Nothing
		// inspects this reason with errors.Is — it is read by a person,
		// and the Config reports "no token" through a field rather than
		// through an error's identity.
		return fmt.Errorf(
			"the config file records %q as the server its token was issued against, "+
				"and that cannot be read as an address (%s), so the token is not being "+
				"reused — you will be asked to log in again",
			redactUserinfo(stored), redactUserinfo(err.Error()))
	}

	effectiveKey, err := api.CanonicalKey(effective)
	if err != nil {
		return fmt.Errorf(
			"this run is talking to %q, which cannot be read as an address (%s), so "+
				"the stored token is not being reused — you will be asked to log in "+
				"again", redactUserinfo(effective), redactUserinfo(err.Error()))
	}

	if storedKey != effectiveKey {
		return fmt.Errorf(
			"the stored token was issued against %s and this run is talking to %s, "+
				"so it is not being reused — you will be asked to log in again",
			redactUserinfo(stored), redactUserinfo(effective))
	}
	return nil
}

// authorityMark is what starts a URL's authority. Written as the two
// slashes alone rather than with the scheme separator, because a literal
// carrying that separator is a compiled-in URL as far as this
// repository's own guard is concerned — correctly, and the guard is not
// loosened for the code that trips it.
const authorityMark = "//"

// userinfoPlaceholder stands in for a username and password. It replaces
// them rather than dropping them, so a reader can still see that the URL
// carried credentials — a silently shortened URL is a second, quieter
// way of telling somebody something untrue about their own file.
const userinfoPlaceholder = "[redacted]"

// redactUserinfo replaces the userinfo of every URL-shaped substring
// with a placeholder. It is the ONE helper every printed URL in this
// package goes through.
//
// The canonical form this package compares against refuses a URL
// carrying a username or password, and argues that doing so keeps a
// credential out of a comparison string. It was in the PRINTED string
// instead — measured twice inside one report, once from the stored value
// and once from inside the wrapped error explaining why it could not be
// read. A comparison string is seen by nobody; a printed one is in a
// terminal, a CI log and a screenshot.
//
// It works on whole MESSAGES rather than on URLs alone, which is why
// there is one helper and not two. Half of the leak arrived inside an
// error string built somewhere else, and a helper that only accepted a
// bare URL would have missed exactly that half.
func redactUserinfo(s string) string {
	var b strings.Builder
	rest := s
	for {
		start := strings.Index(rest, authorityMark)
		if start < 0 {
			b.WriteString(rest)
			return b.String()
		}
		start += len(authorityMark)
		b.WriteString(rest[:start])
		rest = rest[start:]

		end := strings.IndexFunc(rest, endsAuthority)
		if end < 0 {
			end = len(rest)
		}
		authority := rest[:end]
		// The LAST at-sign, not the first: a password may contain one,
		// and only the last can be the separator.
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			b.WriteString(userinfoPlaceholder)
			b.WriteString(authority[at:])
		} else {
			b.WriteString(authority)
		}
		rest = rest[end:]
	}
}

// endsAuthority reports whether r ends a URL's authority.
//
// The set is deliberately generous. None of these can appear in a host
// or a port, and the message this runs over has the URL embedded in
// prose and usually in quotes, so stopping at any of them is right and
// stopping early is harmless — what is between the slashes and the last
// at-sign is what gets replaced.
func endsAuthority(r rune) bool {
	switch r {
	case '/', '?', '#', '"', '\'', '`', ' ', '\t', '\n', ',', '(', ')', ';':
		return true
	}
	return false
}

// renameFile is os.Rename, reached through a variable so the failure that
// makes the atomic write worth having can actually be exercised. A crash
// or an error between "the new file exists" and "the new file is the
// config" is the whole reason for the temp-file-and-rename dance, and a
// claim about it that no test can reach is a claim nobody has checked.
var renameFile = os.Rename

// The modes the two kinds of directory get, and they are different on
// purpose — see createConfigDir.
const (
	ownDirMode    = 0o700
	sharedDirMode = 0o755
)

// createConfigDir creates every missing level of dir, and corrects the
// mode of each level it created — ONLY of those.
//
// # Why this is not one MkdirAll
//
// The config file's platform default is two levels deep: a config root
// shared with every other tool, and this tool's own directory inside it.
// On an account where nothing has used the shared root yet, both are
// missing, and the mode a directory is CREATED with is masked by the
// process umask exactly as a file's is. MkdirAll applies that mask to
// every level it makes, so under a mask that removes the owner's execute
// or write bit the outer level comes out unusable — and then the inner
// mkdir fails outright with a permission error. Correcting the leaf
// afterwards cannot help: the leaf is what could not be created. So each
// level is corrected as soon as it exists, which a single MkdirAll gives
// no place to do.
//
// # Why the levels get different modes
//
// The leaf is ours and it holds a bearer token, so 0700: a directory
// anyone can list is a token anyone can find. Every level ABOVE it is
// shared — other tools keep their own configuration there — and
// narrowing a directory this tool does not own is changing something
// that was not its business. A level created here therefore gets the
// mode creating it by hand under an ordinary mask would have produced.
//
// # A level that was already there is never touched
//
// Which levels exist is noted BEFORE anything is created, because
// afterwards there is no way to tell a directory this code made from one
// that was already there — and that difference is the whole of whose
// mode it is to set. A stat that fails for any reason OTHER than "not
// there" leaves the question unanswered rather than answered "absent",
// and an unanswered question is refused: guessing here means either
// re-permissioning a stranger's directory or failing to permission our
// own.
func createConfigDir(dir string) error {
	var missing []string
	for level := dir; ; {
		_, err := os.Stat(level)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf(
				"could not tell whether the config directory %s already exists: %w — "+
					"fix the permissions on it or on a directory above it, or set %s "+
					"to a full path somewhere this command can write", level, err,
				envConfigPath)
		}
		missing = append(missing, level)
		parent := filepath.Dir(level)
		if parent == level {
			break
		}
		level = parent
	}

	// Shallowest first, so the correction to one level lands before the
	// mkdir that needs it. missing[0] is the leaf.
	for i := len(missing) - 1; i >= 0; i-- {
		level := missing[i]
		mode := os.FileMode(sharedDirMode)
		if i == 0 {
			mode = ownDirMode
		}
		if err := os.Mkdir(level, mode); err != nil {
			// Somebody else created it between the survey above and
			// here. It is then not ours, so its mode is not ours to set
			// either, and the loop carries on into it.
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return fmt.Errorf("could not create the config directory %s: %w", level, err)
		}
		if err := os.Chmod(level, mode); err != nil {
			return fmt.Errorf(
				"could not set the permissions of the config directory %s: %w", level, err)
		}
	}
	return nil
}

// Save writes a credential to c.Path atomically, with the file mode this
// token deserves.
//
// # It takes the PAIR, and that is the shape rather than a convenience
//
// A token and the endpoint it was ISSUED AGAINST are one fact, so they
// arrive together at one entry point and are validated together. Load
// hands a stored token back only when the endpoint recorded beside it
// canonicalises AND matches the run, so a file holding one without the
// other is guaranteed useless — and by the time anything notices, it has
// already replaced a file that was not.
//
// Three caller mistakes used to pass a save and be rejected by the next
// load. They are not caught here; they are UNCONSTRUCTIBLE, because no
// order of field assignments feeds this call:
//
//   - a token that is empty or only whitespace, which the next run
//     reports as a file with nothing in it;
//   - a forgotten endpoint, which makes every later run say the file
//     does not record which server its token came from — for ever;
//   - the realistic one: reusing the *Config that Load just returned,
//     setting a NEW token on it and saving, so the file records the new
//     token against the OLD endpoint and every run afterwards is a
//     mismatch. That is a loop with a perfect-looking file, and it is
//     the reason the endpoint is an ARGUMENT rather than a field this
//     call reads.
//
// Both endpoint mistakes are hard errors here, and the asymmetry Load
// draws does not apply: on the read side an empty stored endpoint can
// only be a caller's bug while an unparseable one most likely came from
// the user's own environment, so one is fatal and the other is a soft
// mismatch. On the write side both can only be the caller's.
//
// # Load-then-modify is THE saving flow
//
// Load, then Save on the value it returned. That is not a suggestion:
// a Config built fresh has no unknown-fields map, so the promise that an
// older binary preserves a newer one's field is true only of a value
// that was READ — the fields it is preserving are the ones the read put
// there. It also carries the file's own schema version, which is what
// stops this build quietly rewriting a newer file as its own.
//
// A fresh &Config{} is still a legitimate way to write a first config,
// and it does exactly what it says: it writes the three fields this
// build knows and claims nothing about any others, because it read none.
//
// The shape, and why each step is there:
//
//   - EVERY missing directory level is created and then corrected, and
//     only the levels this call created. mkdir's mode argument is masked
//     by the process umask exactly as open's is, so a restrictive umask
//     produces a level nothing can be created inside — and the fix has
//     to arrive before the next level is attempted rather than after the
//     last one. A directory that was already there is left alone: it may
//     be one the user pointed us at, and re-permissioning somebody
//     else's directory is not this tool's business. See createConfigDir.
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
//     THIS LINE IS PAPER AND STAYS PAPER, recorded here so the next
//     reader of a green suite knows which claim it is not making.
//     Nothing observable distinguishes a synced write from an unsynced
//     one without cutting the power: both produce the same bytes, the
//     same mode and the same rename. A row asserting that no row can be
//     written would be a guard for a guard, so there is none, and this
//     sentence is the record instead.
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
func (c *Config) Save(token ui.Secret, issuedAgainst string) error {
	if strings.TrimSpace(string(token)) == "" {
		return errors.New(
			"refusing to write a config file with no usable token in it: the file " +
				"exists to hold one, a token that is empty or only whitespace is " +
				"exactly what the next run reports as unusable, and writing it over a " +
				"good file loses the good one")
	}
	if _, err := api.CanonicalKey(issuedAgainst); err != nil {
		return fmt.Errorf(
			"refusing to write a config file that does not say which server its token "+
				"was issued against: %s — without it the next run cannot tell whether "+
				"the stored token belongs to it, so it would ask for a login every "+
				"time and the file would look perfectly correct while it did",
			redactUserinfo(err.Error()))
	}
	if c.Version != 0 && c.Version != SchemaVersion {
		return fmt.Errorf(
			"refusing to rewrite the config file at %s: it records schema version %d "+
				"and this build writes version %d, so saving would replace a version "+
				"number with a claim about the file that is not true. The load path "+
				"declined to repair this file; writing it a moment later is the same "+
				"repair with a different name", c.Path, c.Version, SchemaVersion)
	}

	if c.Path == "" {
		p, err := Path()
		if err != nil {
			return err
		}
		c.Path = p
	}

	dir := filepath.Dir(c.Path)
	if err := createConfigDir(dir); err != nil {
		return err
	}

	data, err := c.marshal(token, issuedAgainst)
	if err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, tempPattern)
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
	sweepTempLitter(dir)

	// The Config describes the file, so it is brought into line with it
	// — and only now. A Save that failed leaves the value exactly as it
	// was, so a caller cannot end up holding a Config that claims a
	// credential is stored when nothing was written.
	c.Token = token
	c.APIURL = issuedAgainst
	return nil
}

// tempPattern is the name the temp file is created under, and the
// pattern the sweep looks for. One constant, because a sweep that
// searched for a shape the writer had stopped using would find nothing
// and report success.
const tempPattern = ".config-*.json"

// sweepTempLitter removes this package's own leftover temp files from
// dir. BEST EFFORT: it never reports, and it never fails a save.
//
// # What it is for
//
// The deferred remove in Save covers the ERROR path. A crash between the
// write and the rename is a different thing and a defer never runs for
// it — what is left is a 0600 file holding the complete token, under a
// name nothing ever looks at again, so it outlives every later rotation
// of the credential inside it.
//
// # The residue, stated rather than implied
//
// A crash DURING the sweep leaves litter, and this makes no attempt to
// be atomic about tidying. A failed remove is ignored, because the file
// being written is worth more than the files being tidied and there is
// nothing useful to tell a user about either.
//
// It also runs after the rename rather than before it, which is the
// deliberate half of a real trade: a second process writing its own
// config at the same instant has a temp file in this directory, and this
// would remove it. That costs the other process a failed save and
// nothing else — its rename fails, its own original is untouched — where
// sweeping first would risk the same thing while ALSO leaving this
// call's litter behind if it went on to fail.
func sweepTempLitter(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		// A directory whose name matches is not litter this made, and
		// removing one needs a different call anyway.
		if entry.IsDir() {
			continue
		}
		if matched, err := filepath.Match(tempPattern, entry.Name()); err == nil && matched {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// marshal renders the file's bytes: every field this build does not know
// about, with the three it does written over the top.
//
// The merge is done as a map rather than by marshalling a struct, and
// that is not a style choice. It is the ONLY place unknown fields can be
// preserved, and it is also the exact spot where marshalling a struct
// that held a ui.Secret would put the redaction placeholder into the
// file. Both reasons point at the same code; see fileConfig.
func (c *Config) marshal(token ui.Secret, issuedAgainst string) ([]byte, error) {
	out := make(map[string]json.RawMessage, len(c.Unknown)+3)
	for k, v := range c.Unknown {
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
		keyToken:  string(token),
		keyAPIURL: issuedAgainst,
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
