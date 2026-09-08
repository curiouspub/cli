package preflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// packageJSONName is the file two of this package's checks read. Named
// once so they cannot come to disagree about it, and so the string a
// message shows the user is the string that was opened.
const packageJSONName = "package.json"

// maxPackageJSONBytes bounds how much of a package.json this package
// will ever read. A megabyte is far past any real one — the largest in
// a monorepo of hundreds of packages is a few tens of kilobytes — and a
// file past it is not a manifest, so the checks report that they could
// not read it rather than pulling an accidental blob into memory.
const maxPackageJSONBytes = 1 << 20

// packageJSONFault says what stopped a package.json being read, or that
// nothing did.
//
// UNREADABLE IS THE ZERO VALUE, deliberately, and for the same reason
// the presence type's zero value is "couldn't tell": a value nobody
// filled in then reports a problem the reader is told about, rather than
// reporting a clean read of a file nobody opened. The safe direction for
// an omission is the one that says less.
type packageJSONFault int

const (
	packageJSONUnreadable packageJSONFault = iota
	packageJSONFound
	packageJSONMissing
	packageJSONInvalid
	packageJSONNotAnObject
)

// packageJSONFile is one attempt to read the project's package.json:
// its top-level fields, or why there are none.
//
// The fields are kept as raw messages rather than decoded into a struct
// because the only questions asked of them are about presence — is there
// a dependencies object, does it list astro — and a struct would decide
// those questions by Go's own field-matching rules instead. See
// declaresAstro for why that distinction is load-bearing here.
type packageJSONFile struct {
	fields map[string]json.RawMessage
	fault  packageJSONFault

	// position locates a parse failure for a person: "line 4, column 3".
	// It is empty when the decoder gave no usable offset, and every
	// message built from it copes with that rather than printing a
	// position of zero.
	position string
}

// readPackageJSON reads and decodes the project's package.json.
//
// isFile RUNS FIRST, and it is not an optimisation. A path that exists
// and is not a regular file — a named pipe with this name is the case
// that has actually been met in this package — makes Open block with no
// writer ever connecting and no timeout anywhere here to notice. The
// three-valued answer is also what keeps "I looked and it is not there"
// apart from "I could not look", which is the difference between the
// message that asks whether this is the right folder and the one that
// asks whether the file is readable.
func readPackageJSON(fsys FS, root string) packageJSONFile {
	name := filepath.Join(root, packageJSONName)

	switch isFile(fsys, name) {
	case absent:
		return packageJSONFile{fault: packageJSONMissing}
	case undetermined:
		return packageJSONFile{fault: packageJSONUnreadable}
	}

	data, err := readCapped(fsys, name, maxPackageJSONBytes)
	if err != nil {
		return packageJSONFile{fault: packageJSONUnreadable}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return decodeFault(data, err)
	}

	// A bare `null` decodes into a nil map WITHOUT an error, so the only
	// thing that distinguishes it from an empty object is this check.
	// Left out, a package.json containing the four characters "null"
	// would be reported as a valid manifest that happens to list no
	// dependencies — a confident wrong claim about a file nothing can
	// install from.
	if fields == nil {
		return packageJSONFile{fault: packageJSONNotAnObject}
	}
	return packageJSONFile{fields: fields, fault: packageJSONFound}
}

// decodeFault turns a decoder error into the fault a person is told
// about, WITHOUT letting the Go error string through.
//
// The offset is the decoder's and the sentence is ours, which is the
// whole of it: the standard library's message names a Go type and a byte
// count, and neither is something the reader of a pre-flight failure can
// act on. A line and a column are.
func decodeFault(data []byte, err error) packageJSONFile {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return packageJSONFile{fault: packageJSONInvalid, position: jsonPosition(data, syntax.Offset)}
	}

	// The file parsed as JSON and is not an object — an array, a string,
	// a number. That is a different thing to tell somebody than "this
	// isn't valid JSON", because the fix is different.
	var wrongType *json.UnmarshalTypeError
	if errors.As(err, &wrongType) {
		return packageJSONFile{fault: packageJSONNotAnObject}
	}
	return packageJSONFile{fault: packageJSONInvalid}
}

// jsonPosition turns a decoder's byte offset into a line and column a
// person can find in an editor, or "" when the offset names nowhere in
// the data.
//
// IT COUNTS BYTES, not display columns, and says so because the two
// differ on any line holding a tab or a character outside ASCII. A byte
// column still lands the reader on the right line and close enough
// along it to see the problem, which is the whole job; a display column
// would mean deciding how wide a tab is, and being wrong about it in
// somebody else's editor.
func jsonPosition(data []byte, offset int64) string {
	// The decoder's offset is a count of bytes consumed, so the byte the
	// complaint is about sits at offset-1. An offset of zero or one
	// names the very start, and anything past the data names nowhere.
	if offset < 1 || offset > int64(len(data))+1 {
		return ""
	}

	line, column := 1, 1
	for i := int64(0); i < offset-1 && i < int64(len(data)); i++ {
		if data[i] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return fmt.Sprintf("line %d, column %d", line, column)
}

// dependencySections are the package.json keys the astro-dep check
// consults, and the WIDENING is the decision recorded here.
//
// The pre-flight table says "dependencies". Checking devDependencies too
// is deliberate: the scaffolding tool puts astro in dependencies, but a
// large minority of real projects keep build tooling in devDependencies,
// and the build agent's install covers both. Refusing those would be a
// hard stop on a project that builds perfectly well, which is the exact
// failure this whole set of checks is shaped to avoid. Nothing on the
// wire and nothing on the server changes with it, so it is a client-side
// judgement made here and written down here.
//
// peerDependencies and optionalDependencies are NOT consulted, and this
// check does not walk up the tree. Hoisting changes where a dependency
// is INSTALLED, not where it is DECLARED — in a workspace each package
// declares its own — so a workspace site package does list astro and
// passes this check. What a workspace layout breaks is the lockfile
// question, and that is answered next door.
var dependencySections = []string{"dependencies", "devDependencies"}

// astroPackageName is the dependency the astro-dep check looks for.
const astroPackageName = "astro"

// declaresAstro reports whether package.json lists astro in either
// section this check consults.
//
// KEY CASE, STATED BECAUSE THE NEXT READER WILL ASSUME OTHERWISE. The
// decode is into a map, so every key here is matched EXACTLY, byte for
// byte. Decoding into a struct would not: the standard library fills
// struct fields by Unicode simple fold, so a file spelling
// "DEPENDENCIES" would populate a Dependencies field. This program
// elsewhere REFUSES a file whose keys collide under that fold — and that
// ruling does not transfer here, because that file is written by this
// program and this one is written by the user. A package.json spelling
// its key some other way is not one the package manager installs from
// either, and refusing somebody's working project over key case would be
// the false hard stop this check exists to avoid. So the lookup is
// exact, and a key that is not the one the ecosystem reads is simply not
// consulted.
func declaresAstro(fields map[string]json.RawMessage) bool {
	for _, section := range dependencySections {
		raw, ok := fields[section]
		if !ok {
			continue
		}

		// A section that is not an object declares nothing this check
		// can read, and that is reported as "astro is not listed"
		// rather than as a parse failure. It is the honest answer: the
		// claim the check makes is about what package.json declares,
		// and a dependencies field holding a string declares no
		// packages at all.
		var declared map[string]json.RawMessage
		if err := json.Unmarshal(raw, &declared); err != nil {
			continue
		}
		if _, ok := declared[astroPackageName]; ok {
			return true
		}
	}
	return false
}

// declaresWorkspaces reports whether the package.json at name carries a
// workspaces field — one of the three signals that this project sits
// inside a monorepo whose lockfile lives somewhere above it.
//
// A MISSING OR UNREADABLE FILE IS NOT A SIGNAL, and this returns false
// for both. The caller is choosing which of two messages to print, and
// the fallback message is the correct one whenever a workspace has not
// been positively established — so "I could not look" and "there is
// nothing here" lead to the same place, and neither is dressed up as the
// other.
func declaresWorkspaces(fsys FS, name string) bool {
	if isFile(fsys, name) != present {
		return false
	}
	data, err := readCapped(fsys, name, maxPackageJSONBytes)
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return false
	}
	_, ok := fields["workspaces"]
	return ok
}
