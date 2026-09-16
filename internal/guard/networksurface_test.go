package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE NETWORK SURFACE, ENUMERATED FROM SOURCE.
//
// The README tells a stranger what a run of this program talks to, and
// that sentence is the whole trust argument of a repository whose first
// hard rule is that it phones home to nobody. Until this row existed the
// sentence was prose beside code, checked by reading.
//
// IT IS A CLAIM ABOUT SOURCE AND NOT A RUNTIME OBSERVATION, and saying
// so is the point rather than a hedge. What it proves is that this
// module NAMES exactly these paths and opens exactly one upload; it does
// not watch a run and count connections, so a request built somewhere
// this walk cannot resolve — a path assembled from pieces, a host read
// out of a file — is outside it. The rows that close THAT gap need a
// transport every call is made through and a test that counts what
// crosses it. That seam is deliberately not built here: it is a change to
// how this program is wired rather than a check over how it is written,
// and a row that implied it existed would be the more dangerous of the
// two errors.
//
// WHAT IT DOES CATCH is the shape a second destination actually takes in
// a codebase like this one: a new path literal, a call to somewhere that
// is not the API, or a second uploader. Each of those is a line somebody
// writes, and each of them reds here.

// apiPathLiterals is every /v1 path this module is allowed to name, and
// what each is for.
//
// THE REASONS ARE HERE BECAUSE THE SET IS THE ASSERTION. A bare list
// invites the next person to add a line to make their change green; a
// list where every entry says what it is for makes that an edit somebody
// has to justify in the same diff.
var apiPathLiterals = map[string]string{
	"/v1/capacity":    "asking whether there is room today, before any account-creating work",
	"/v1/waitlist":    "the signup offered when the door is shut",
	"/v1/auth/start":  "the first step of the email-and-code login",
	"/v1/auth/verify": "the second step, which returns the credential every later call spends",
	"/v1/deploys":     "the create: a deploy record, and somewhere to send the archive",
	"/v1/deploys/":    "the prefix every per-deploy path is built from",
}

// deployPathActions is every per-deploy endpoint, named by the action
// that completes the path.
var deployPathActions = map[string]string{
	"start":   "turning an uploaded archive into a running build",
	"publish": "giving a built deploy the address it answers at",
	"events":  "the build log, which is the only long-lived connection here",
}

// apiPackage is where a path may be named, and uploadSite is where the
// one upload lives. Both are slash-separated and relative to the module
// root, because a site key is a property of the repository rather than
// of the host reading it.
const (
	apiPackage = "internal/api"
	uploadSite = "internal/flow/upload.go"
)

// networkSurface is what one walk of the published sources found.
type networkSurface struct {
	// paths maps each /v1 literal to the files naming it.
	paths map[string][]string
	// actions maps each deployPath action to the files naming it.
	actions map[string][]string
	// puts maps each file opening a PUT to how many it opens.
	puts map[string]int
	// scanned is how many files were read, so an empty walk cannot pass.
	scanned int
}

// readNetworkSurface walks every published non-test Go file and records
// what it says about where this program sends things.
func readNetworkSurface(t *testing.T, root string) networkSurface {
	t.Helper()
	surface := networkSurface{
		paths:   map[string][]string{},
		actions: map[string][]string{},
		puts:    map[string]int{},
	}
	fset := token.NewFileSet()
	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		rel := displayPath(root, path)
		surface.scanned++

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(node.Value)
				if err != nil || !strings.HasPrefix(value, "/v1") {
					return true
				}
				surface.paths[value] = appendOnce(surface.paths[value], rel)

			case *ast.CallExpr:
				// The per-deploy endpoints finish their path with an
				// action, so the action is the half a literal scan cannot
				// see.
				if calleeName(node.Fun) == "deployPath" && len(node.Args) == 2 {
					if lit, ok := node.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if action, err := strconv.Unquote(lit.Value); err == nil {
							surface.actions[action] = appendOnce(surface.actions[action], rel)
						}
					}
				}

			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "http" &&
					node.Sel.Name == "MethodPut" {
					surface.puts[rel]++
				}
			}
			return true
		})
	}
	if surface.scanned == 0 {
		t.Fatal("no published non-test Go file was read, so this row is green about nothing")
	}
	return surface
}

func appendOnce(list []string, value string) []string {
	for _, got := range list {
		if got == value {
			return list
		}
	}
	return append(list, value)
}

func sortedStringKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// TestTheNetworkSurfaceIsTheAPIAndOneUploadPerDeploy holds the README's
// claim about what a run talks to, to the source that would have to
// change for it to stop being true.
func TestTheNetworkSurfaceIsTheAPIAndOneUploadPerDeploy(t *testing.T) {
	root := moduleRoot(t)
	surface := readNetworkSurface(t, root)

	// THE PATHS, BOTH DIRECTIONS. A path named and not declared here is a
	// destination nobody wrote down; a path declared and no longer named
	// is a list that has stopped describing the program, and the second
	// is how a set like this quietly becomes decoration.
	for _, path := range sortedStringKeys(surface.paths) {
		if _, declared := apiPathLiterals[path]; !declared {
			t.Errorf("%s names the path %q, which this module does not declare.\n"+
				"Every address this program sends to is either the API's own or the one the "+
				"API hands back for a single deploy. A new path is a new destination, and it "+
				"belongs in the declared set with what it is for — in the change that adds it.",
				strings.Join(surface.paths[path], ", "), path)
		}
	}
	for _, path := range sortedStringKeys(apiPathLiterals) {
		if len(surface.paths[path]) == 0 {
			t.Errorf("no published source names the declared path %q (%s), so the declaration "+
				"has stopped describing this program", path, apiPathLiterals[path])
		}
	}

	// EVERY PATH IS NAMED IN ONE PACKAGE. A path literal anywhere else is
	// a second place that knows how to reach the service, which is how a
	// client grows a call nothing routes through its own address guard.
	for _, path := range sortedStringKeys(surface.paths) {
		for _, file := range surface.paths[path] {
			if !strings.HasPrefix(file, apiPackage+"/") {
				t.Errorf("%s names the API path %q, and only %s may.\n"+
					"A path outside that package is a request that does not go through the "+
					"client every other call is made with.", file, path, apiPackage)
			}
		}
	}

	// THE PER-DEPLOY ACTIONS, which complete a path a literal scan sees
	// only the prefix of.
	for _, action := range sortedStringKeys(surface.actions) {
		if _, declared := deployPathActions[action]; !declared {
			t.Errorf("%s completes a per-deploy path with the action %q, which is not declared",
				strings.Join(surface.actions[action], ", "), action)
		}
	}
	for _, action := range sortedStringKeys(deployPathActions) {
		if len(surface.actions[action]) == 0 {
			t.Errorf("no published source uses the declared per-deploy action %q (%s)",
				action, deployPathActions[action])
		}
	}

	// ONE UPLOAD, IN ONE PLACE. The archive is the only thing this
	// program sends anywhere other than the API, and the address it goes
	// to is one the API just handed back. A second PUT site is a second
	// answer to "where does the project go", and the two could differ.
	sites := sortedStringKeys(surface.puts)
	switch {
	case len(sites) == 0:
		t.Error("no published source opens a PUT at all, so the upload this row is about " +
			"either moved or changed method — and the README's claim about what a run sends " +
			"is no longer described by anything here")
	case len(sites) > 1:
		t.Errorf("%d files open a PUT: %s\nThe packed archive is the one thing that goes "+
			"anywhere but the API, and it goes to an address the API hands back for that "+
			"deploy alone. A second uploader is a second destination.",
			len(sites), strings.Join(sites, ", "))
	case sites[0] != uploadSite:
		t.Errorf("the one PUT is opened in %s and the upload lives in %s", sites[0], uploadSite)
	case surface.puts[sites[0]] != 1:
		t.Errorf("%s opens %d PUTs, want exactly one — the upload is one request per deploy",
			sites[0], surface.puts[sites[0]])
	}

	if !t.Failed() {
		t.Logf("%d published files: %d declared API paths, %d per-deploy actions, one upload in %s",
			surface.scanned, len(apiPathLiterals), len(deployPathActions), uploadSite)
	}
}
