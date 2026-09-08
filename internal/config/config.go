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
	"time"

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

// ambiguousSpellings reports a set of top-level keys the file format
// treats as ONE field but the file spells more than once, or nil when
// every key in the file is distinct under that relation.
//
// # It asks about every key, not only the ones this build knows
//
// The narrow version — checking only the fields this build understands —
// leaves the identical trap sitting in the file for whichever release
// learns a fourth. That release ships, meets a file carrying two
// fold-equal spellings of its new field, fills the struct from one and
// preserves the other, and is back to a value that is used AND written
// back. Nothing in the binaries already released could have warned
// anybody, because they are already released. So the question is asked
// of every key, and the cost is accepted and stated: a file this build
// could have round-tripped faithfully is refused. Two keys the format
// folds together are ambiguous to any decoder that knows the field, so
// this refuses early rather than wrongly.
//
// # An EXACT duplicate is not this, and cannot be
//
// A file spelling one key twice byte for byte — two "token" entries —
// is invisible here, and to any check written at this level. Both the
// struct decoder and the map decoder take the LAST occurrence, so by the
// time either result exists the multiplicity is gone: the map holds one
// entry and the struct holds one value, and they agree. Seeing it would
// mean re-tokenising the file's bytes rather than reading its decoded
// form. Accepted with this note, and the consequence is mild by
// comparison — the two decoders cannot DISAGREE about an exact
// duplicate, which is the failure this whole check exists for.
//
// # Why the comparison is pairwise rather than bucketed by a key
//
// Grouping would need a canonical fold form, and any such form is a
// second implementation of the relation that can drift from it. A
// pairwise strings.EqualFold cannot drift, because it IS the relation. A
// config file's top-level fields number in the handful, so the quadratic
// shape costs nothing worth naming.
//
// The keys are sorted first and the first group found is returned, so
// the report is the same on every run; a map's iteration order is not
// something a user should see change underneath them while they are
// trying to fix their file.
func ambiguousSpellings(raw map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for i, key := range keys {
		group := []string{key}
		for _, other := range keys[i+1:] {
			if strings.EqualFold(key, other) {
				group = append(group, other)
			}
		}
		if len(group) > 1 {
			return group
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
	//
	// THE CONTRACT, because the next caller needs it before it writes a
	// branch: this value is FOR A PERSON TO READ. A caller that has to
	// behave differently for different causes branches on a FIELD — Token
	// being empty, PermissionsChecked, Version — and never on this
	// error's identity. It does not wrap: the wrapped forms quoted a
	// stored URL a second time, which is how a password in an api_url
	// reached one message twice, so the cause is rendered into the text
	// and errors.Is has nothing here to find. If a flow ever genuinely
	// needs to distinguish two causes programmatically, the answer is a
	// new field on this struct, not a sentinel behind this one.
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
//
// CURIOUS_CONFIG gets no such treatment: it is honoured EXACTLY as
// given, including a relative path and including one starting with a
// tilde. The documentation asks for a full path, and the two rules
// differ because the variables do. The base-directory one is a
// standard's, read out of an environment this program did not set up and
// shared with every other tool; this one is an escape hatch somebody
// typed for this command on purpose, and second-guessing it would mean
// writing the token somewhere other than where they said. A tilde is
// expanded by a shell before the program ever sees it, so one that
// survives into this value was quoted deliberately and names a directory
// whose first character really is a tilde.
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
// THE ASYMMETRY IN HOW THIS TREATS ITS OWN ARGUMENT is deliberate, and
// it is written down because a reader meets it as an inconsistency. An
// EMPTY effective endpoint ends the call; an unparseable one is a soft
// mismatch that costs a login. Empty can only be a caller that forgot to
// wire the value through, and reporting it as "no token" would hide the
// bug for ever behind a run that simply logs in every time. Garbage most
// likely arrives from the user's own environment variable, where ending
// the run would strand them with no way past it.
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

	fi, statErr := os.Stat(path)
	if statErr == nil && fi.IsDir() {
		// A DIRECTORY GETS ITS OWN BRANCH, because it is the natural
		// mistake rather than an exotic one: the base-directory variable
		// takes a directory, so pointing the file override at one is
		// following the convention. Without this the read fails with the
		// operating system's own "is a directory" and the generic
		// message advises changing the permissions of the directory and
		// then deleting it — two actions, neither of them the problem.
		// The mode check is skipped entirely: the mode of a directory is
		// not the question, and answering it here would be a warning
		// nobody can act on.
		cfg.NoTokenReason = fmt.Errorf(
			"%s names %s, which is a directory rather than a file — it has to be "+
				"the full path to the config file itself, ending in %s. (The "+
				"base-directory variable %s is the one that takes a directory.)",
			envConfigPath, path, fileName, envXDGConfigHome)
		return cfg, nil
	}

	// THE FILE IS READ BEFORE ITS MODE IS JUDGED, and the order is the
	// whole of a small correction: the warning used to say the file
	// "holds an access token" whatever was in it, including a file this
	// package was about to report as holding nothing.
	holdsToken := cfg.read(path, effectiveAPIURL)
	if statErr == nil {
		warning, checked := modeWarning(path, fi, holdsToken)
		cfg.PermissionsChecked = checked
		if warning != "" {
			cfg.Warnings = append(cfg.Warnings, warning)
		}
	}
	return cfg, nil
}

// read fills cfg from the file at path and reports whether that file
// APPEARS TO HOLD A TOKEN — which is a different question from whether
// one came back, and is asked only so the permissions warning does not
// claim something the read already disproved.
//
// When the answer cannot be determined — an unreadable file, a corrupt
// one, a schema from the future — it reports TRUE. Mentioning a token
// that turns out not to be there costs a clause; omitting one that is
// there costs the point of the warning.
func (cfg *Config) read(path, effectiveAPIURL string) bool {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		// No file is the ordinary first run, not a problem: no warning,
		// no reason, nothing for the caller to print.
		return false
	}
	if err != nil {
		cfg.NoTokenReason = fmt.Errorf(
			"could not read the config file at %s: %w — fix the file's permissions, "+
				"or delete it and run this command again to log in from scratch",
			path, err)
		return true
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
		return true
	}
	var fc fileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		cfg.NoTokenReason = corruptError(path, err)
		return true
	}

	// From here the file has parsed, so this is an answer rather than a
	// default.
	holdsToken := strings.TrimSpace(fc.Token) != ""

	// AMBIGUITY FIRST, and the order is forced rather than chosen: you
	// cannot say what version a file claims until you know its keys are
	// unambiguous, because the version field itself is one of the keys
	// two spellings could disagree about. Nothing below this point — not
	// the version, not the endpoint, not the preserved unknown fields —
	// is taken from a file whose own field names disagree about what it
	// says.
	if spellings := ambiguousSpellings(raw); spellings != nil {
		cfg.NoTokenReason = ambiguousError(path, spellings)
		return true
	}

	// ABSENT, ZERO AND NULL ARE THREE DIFFERENT THINGS, and assigning
	// the decoded value only when it was non-zero made them one. Absent
	// is a field that says nothing and is read as this build's own
	// schema. Present-and-null is a field that says nothing while
	// claiming to say something. Present-and-zero is a real number that
	// no release has ever written. The last two are refused: a file
	// claiming a schema that does not exist is not one to guess about,
	// and guessing means reading a stranger's format as though it were
	// ours.
	//
	// The lookup is fold-aware because the decoder's is, and it is safe
	// to take the first match because the ambiguity check above has
	// already refused a file with more than one.
	if encoded, present := rawField(raw, keyVersion); present {
		if fc.Version == 0 {
			cfg.NoTokenReason = malformedVersionError(path, encoded)
			return holdsToken
		}
		cfg.Version = fc.Version
	}

	// A file from a release this build does not know is a file whose
	// every other property this build is not entitled to have an opinion
	// about. It used to be reported as corrupted, which the code already
	// knew was wrong: the map decode had succeeded and the version had
	// been read before the sentence was printed.
	if cfg.Version != SchemaVersion {
		cfg.NoTokenReason = versionError(path, cfg.Version)
		return holdsToken
	}

	cfg.APIURL = fc.APIURL
	stripKnownKeys(raw)
	cfg.Unknown = raw

	if !holdsToken {
		cfg.NoTokenReason = fmt.Errorf(
			"the config file at %s has no token in it — delete the file and run this "+
				"command again to log in", path)
		return false
	}

	if reason := endpointMismatch(fc.APIURL, effectiveAPIURL); reason != nil {
		cfg.NoTokenReason = reason
		return true
	}

	cfg.Token = ui.Secret(fc.Token)
	return true
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

// rawField finds a top-level field by the same fold relation the JSON
// decoder matches struct tags with, so this and the decoder cannot
// disagree about whether a file carries a field.
//
// It returns the FIRST match, which is unambiguous only because
// ambiguousSpellings has already refused any file carrying more than
// one. Calling it before that check would be reading one of two
// spellings and calling it the answer.
func rawField(raw map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	for name, value := range raw {
		if strings.EqualFold(name, key) {
			return value, true
		}
	}
	return nil, false
}

// malformedVersionError is for a version field that is PRESENT and is
// not a version: a literal null, or a zero no release has written.
//
// It quotes the literal back rather than describing it, because the two
// cases suggest different things about how the file got that way — a
// null is usually a serialiser writing an absent value, a zero is
// usually a struct that was never filled in — and the person looking at
// the file can tell those apart when they can see which they have.
func malformedVersionError(path string, encoded json.RawMessage) error {
	return fmt.Errorf(
		"the config file at %s records its schema version as %s, which is not a "+
			"version any release of curious has written — the file looks corrupted, "+
			"so delete it and run this command again to log in from scratch",
		path, encoded)
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
// THE EQUIVALENCE HAS A KNOWN BLIND SPOT, accepted with this note. A
// percent-encoded slash in a path compares equal to a real one, so two
// endpoints spelled /a%2Fb and /a/b are treated as one. They share a
// host, so no token can cross from one issuer to another through it —
// which is the failure this comparison exists to prevent, and the reason
// the collapse is tolerable. It is recorded because a blind spot nobody
// wrote down is one the next reader has to rediscover by finding it in
// the field.
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

// The modes the two kinds of directory are created with, and they are
// treated differently on purpose — see createConfigDir.
//
// ownDirMode is SET, after creation, so the umask cannot widen or narrow
// it. sharedDirMode is only what a level above ours is created with: the
// umask then governs, and nothing here corrects the result, because that
// directory is the user's rather than this command's.
const (
	ownDirMode    = 0o700
	sharedDirMode = 0o755
)

// unusableParentError explains the one failure this design accepts in
// exchange for not touching a directory it does not own.
//
// A umask that strips the owner's execute or write bit makes every
// directory that user creates unusable to them. When this call had to
// create the level above ours, that mask applied to it, and nothing here
// puts the bits back. The operating system reports only "permission
// denied" on the inner mkdir, which points at the wrong directory and
// names no cause — so this names the parent, the mode it actually has,
// and the three things that fix it.
func unusableParentError(parent, child string, err error) error {
	mode := "unknown"
	if fi, statErr := os.Stat(parent); statErr == nil {
		mode = fmt.Sprintf("%04o", fi.Mode().Perm())
	}
	return fmt.Errorf(
		"could not create the config directory %s: %w — this command had to create "+
			"%s on the way there, the process umask took permissions off it as it "+
			"was created (it is now %s), and widening a directory shared with your "+
			"other tools is not this command's decision to make. Create %s yourself "+
			"with the permissions you want, or run this command with a less "+
			"restrictive umask, or set %s to a full path somewhere writable",
		child, err, parent, mode, parent, envConfigPath)
}

// createConfigDir creates every missing level of dir, and sets the mode
// of the LEAF only.
//
// # Nothing above the leaf is chmodded, and that is the rule
//
// The leaf is ours and it holds a bearer token, so it is pinned to 0700
// whatever the umask: a directory anyone can list is a token anyone can
// find. Every level ABOVE it is shared — other tools keep their own
// configuration there — and it belongs to the user, not to this command.
// So those levels are created with the conventional mode and the UMASK
// GOVERNS what that becomes.
//
// An earlier version of this pinned created ancestors to 0755 so that a
// first run would succeed under every mask. It bought that by WIDENING a
// directory a careful user's umask would have made 0700, which is a tool
// repairing its environment by deciding it knows better than the umask.
// Costing a user a message they can act on is the better trade than
// silently loosening a directory they share with everything else they
// run.
//
// # What that costs, and it is real
//
// A umask that strips the owner's execute or write bit makes every
// directory that user creates unusable to them — everywhere, not only
// here. So when the shared root does not exist yet AND the mask is one
// of those, this cannot create the level below it, and says so with the
// mode it actually found rather than passing the operating system's bare
// permission error up. The failure is the umask's and the message names
// it; see the mask table in the row that measures this.
//
// # Why this is not one MkdirAll
//
// MkdirAll gives no place to set the leaf's mode as soon as the leaf
// exists, and no way to tell which levels it created. Both matter here:
// the first because the leaf's mode is not negotiable, the second
// because a level that was already there is somebody else's.
//
// # A level that was already there is never touched
//
// Which levels exist is noted BEFORE anything is created, because
// afterwards there is no way to tell a directory this code made from one
// that was already there. A stat that fails for any reason OTHER than
// "not there" leaves the question unanswered rather than answered
// "absent", and an unanswered question is refused rather than guessed.
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

	// Shallowest first. missing[0] is the leaf.
	for i := len(missing) - 1; i >= 0; i-- {
		level := missing[i]
		if err := os.Mkdir(level, sharedDirMode); err != nil {
			// Somebody else created it between the survey above and
			// here. It is then not ours, and the loop carries on into
			// it.
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			// A level this call created one step earlier is the likely
			// culprit, and the operating system's own error does not
			// say so.
			if i+1 < len(missing) && errors.Is(err, fs.ErrPermission) {
				return unusableParentError(missing[i+1], level, err)
			}
			return fmt.Errorf("could not create the config directory %s: %w", level, err)
		}
		if i == 0 {
			if err := os.Chmod(level, ownDirMode); err != nil {
				return fmt.Errorf(
					"could not restrict the config directory %s to its owner: %w",
					level, err)
			}
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
//   - The mode is set explicitly, and the reason is that A UMASK
//     REMOVES BITS. A create mode is masked by the process umask, so it
//     is a ceiling rather than a setting: under umask 0277 a file asked
//     for at 0600 arrives at 0400 and this package cannot read back what
//     it just wrote. The reason once given here — that the token never
//     exists on disk in a file anyone else could read — is true and
//     holds WITHOUT this line, because the temp file is created at 0600
//     already. Two comments in this package gave two different reasons
//     for one line, and only one of them was the reason.
//
//   - A STRICTER existing mode is kept rather than widened. See
//     fileModeFor: the load path calls a narrower mode nobody else's
//     business, and the save used to undo it on the next login.
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
//
//     WHAT THE RENAME COSTS, stated rather than discovered. If the
//     config path is a SYMLINK, the rename replaces the link itself and
//     the file it pointed at keeps the old token. That is inherent to
//     renaming over a target and there is no version of an atomic write
//     that avoids it — following the link first would reintroduce the
//     truncate this exists to prevent. The person it affects is the one
//     who symlinked their config into a synced dotfiles directory, and
//     who is therefore left with a stale credential sitting in the
//     directory they sync. Worth knowing about; not worth trading the
//     property above for.
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
	// is a ceiling rather than a setting. This is the line that makes
	// the mode exact whatever the umask is — in both directions.
	if err := chmodFile(f, fileModeFor(c.Path)); err != nil {
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
	// Including the version, which is the field the next save's refusal
	// to downgrade a file from the future is decided by. A value that
	// wrote this build's schema and still reported none would be
	// deciding that on something nothing put there.
	c.Version = SchemaVersion
	return nil
}

// configFileMode is what this package writes a config file as: readable
// and writable by its owner and by nobody else.
const configFileMode os.FileMode = 0o600

// fileModeFor returns the mode the file at path should be written with:
// configFileMode, unless the file already there is STRICTER, in which
// case that mode is kept.
//
// The load path warns about a file anyone else can read and deliberately
// says nothing about one that is narrower than this package would write
// — a user who tightened their own config has done nothing wrong. The
// next save then widened it straight back, so the tightening survived
// exactly as far as the next login.
//
// Two limits on "stricter", and both are there to stop this becoming a
// way to write a file nobody wants. A WIDER mode is not preserved: the
// file is being replaced, and 0600 is what it should be. And a mode with
// no owner-read bit is not a stricter setting but a file this package
// could never read back, which is not something to reproduce on purpose.
//
// A symlink is followed here, as it is everywhere else in this call —
// see Save on what a rename does to one.
func fileModeFor(path string) os.FileMode {
	fi, err := os.Stat(path)
	if err != nil {
		return configFileMode
	}
	perm := fi.Mode().Perm()
	if perm&^configFileMode == 0 && perm&0o400 != 0 {
		return perm
	}
	return configFileMode
}

// tempPattern is the name the temp file is created under, and the
// pattern the sweep looks for. One constant, because a sweep searching
// for a shape the writer had stopped using would find nothing and report
// success for ever.
//
// THE PREFIX IS DISTINCTIVE ON PURPOSE, and the first version of this
// was not. It swept ".config-*.json" — the pattern the temp files
// happened to use — so nothing separated a file this process created
// from one that merely matched. Measured after a single save:
// .config-backup.json and .config-2024.json were both deleted. Backing a
// config up under an obvious name beside it is the most natural thing a
// person can do with this file, and they would have found out on the
// login after the one that destroyed it.
const tempPattern = ".curious-config-tmp-*"

// tempLitterMaxAge is how long a file matching tempPattern is left alone
// before the sweep treats it as litter.
//
// It exists for the concurrent writer. A second process saving its own
// config at this instant has an in-flight temp file in this directory,
// and removing it costs that process its save. An hour is far longer
// than any write here takes and far shorter than a leaked token should
// be allowed to sit on disk.
const tempLitterMaxAge = time.Hour

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
// # Three things it will not touch, and why each
//
// A file that does not carry this package's own temp prefix: it is
// somebody's, and a name that merely resembles ours is not evidence that
// we made it. A file younger than tempLitterMaxAge: it may be another
// process's write in progress. A directory: removing one takes a
// different call and would be a different kind of mistake.
//
// # The residue, stated rather than implied
//
// A crash DURING the sweep leaves litter, and this makes no attempt to
// be atomic about tidying. A failed remove is ignored, because the file
// being written is worth more than the files being tidied and there is
// nothing useful to tell a user about either. Litter younger than the
// gate waits for a later save rather than being collected now.
func sweepTempLitter(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matched, err := filepath.Match(tempPattern, entry.Name())
		if err != nil || !matched {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < tempLitterMaxAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
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
