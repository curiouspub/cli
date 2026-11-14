package guard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
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

// controlPathLiterals is the fixed public browser-login contract. The
// decision route is advertised for the browser; the native client never
// calls it to approve a request.
var controlPathLiterals = map[string]string{
	"/v1/auth/client/capabilities": "discovering the fixed browser-login exchange",
	"/v1/auth/client/requests":     "creating or recovering a proof-bound request",
	"/v1/auth/client/decision":     "advertising the browser-only decision endpoint",
	"/v1/auth/client/poll":         "polling an approved request with its proof",
	"/v1/auth/client/session":      "advertising credential validation",
	"/v1/auth/client/revoke":       "revoking the selected credential",
}

const controlContractFile = "pkg/controlwire/auth.go"

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
	surface := readNetworkSurface(t, moduleRoot(t))
	for _, problem := range networkSurfaceFindings(surface) {
		t.Error(problem)
	}
	if !t.Failed() {
		t.Logf("%d published files: %d declared API paths, %d per-deploy actions, one upload in %s", surface.scanned, len(apiPathLiterals)+len(controlPathLiterals), len(deployPathActions), uploadSite)
	}
}

// networkSurfaceFindings is shared by the live check and adversarial
// fixtures, so a passing negative test exercises the same policy.
func networkSurfaceFindings(surface networkSurface) []string {
	var problems []string
	for _, path := range sortedStringKeys(surface.paths) {
		_, legacy := apiPathLiterals[path]
		_, control := controlPathLiterals[path]
		if !legacy && !control {
			problems = append(problems, fmt.Sprintf("undeclared API path %q in %s", path, strings.Join(surface.paths[path], ", ")))
		}
	}
	for _, declared := range []map[string]string{apiPathLiterals, controlPathLiterals} {
		for _, path := range sortedStringKeys(declared) {
			if len(surface.paths[path]) == 0 {
				problems = append(problems, fmt.Sprintf("declared API path %q was not found", path))
			}
		}
	}
	for _, path := range sortedStringKeys(surface.paths) {
		_, control := controlPathLiterals[path]
		for _, file := range surface.paths[path] {
			if control {
				if file != controlContractFile {
					problems = append(problems, fmt.Sprintf("Control path %q must be named only in %s, found %s", path, controlContractFile, file))
				}
			} else if !strings.HasPrefix(file, apiPackage+"/") {
				problems = append(problems, fmt.Sprintf("API path %q must be named only in %s, found %s", path, apiPackage, file))
			}
		}
	}
	for _, action := range sortedStringKeys(surface.actions) {
		if _, ok := deployPathActions[action]; !ok {
			problems = append(problems, fmt.Sprintf("undeclared per-deploy action %q", action))
		}
	}
	for _, action := range sortedStringKeys(deployPathActions) {
		if len(surface.actions[action]) == 0 {
			problems = append(problems, fmt.Sprintf("declared per-deploy action %q was not found", action))
		}
	}
	sites := sortedStringKeys(surface.puts)
	switch {
	case len(sites) == 0:
		problems = append(problems, "no upload PUT was found")
	case len(sites) > 1:
		problems = append(problems, fmt.Sprintf("%d files open a PUT: %s", len(sites), strings.Join(sites, ", ")))
	case sites[0] != uploadSite:
		problems = append(problems, fmt.Sprintf("upload PUT found in %s, expected %s", sites[0], uploadSite))
	case surface.puts[sites[0]] != 1:
		problems = append(problems, fmt.Sprintf("%s opens %d PUTs, expected one", sites[0], surface.puts[sites[0]]))
	}
	return problems
}

// Each case compiles Go source in a disposable Git repository, then runs
// the same literal discovery and policy used by the real-tree check.
func TestNetworkSurfacePolicyAdversarial(t *testing.T) {
	legacy := []string{"/v1/capacity", "/v1/waitlist", "/v1/auth/start", "/v1/auth/verify", "/v1/deploys", "/v1/deploys/"}
	control := []string{"/v1/auth/client/capabilities", "/v1/auth/client/requests", "/v1/auth/client/decision", "/v1/auth/client/poll", "/v1/auth/client/session", "/v1/auth/client/revoke"}
	cases := []struct {
		name, extraFile, extraSource, remove, removeAction, want string
		appendFile, omitPut                                      bool
	}{
		{name: "positive"},
		{name: "unknown Control route", extraFile: "internal/api/extra.go", extraSource: `package api; const bad = "/v1/auth/client/unexpected"`, want: `undeclared API path "/v1/auth/client/unexpected"`},
		{name: "unknown other route", extraFile: "internal/api/extra.go", extraSource: `package api; const bad = "/v1/unexpected"`, want: `undeclared API path "/v1/unexpected"`},
		{name: "Control route in API", extraFile: "internal/api/extra.go", extraSource: `package api; const bad = "/v1/auth/client/poll"`, want: `found internal/api/extra.go`},
		{name: "Control route elsewhere", extraFile: "other/extra.go", extraSource: `package other; const bad = "/v1/auth/client/poll"`, want: `found other/extra.go`},
		{name: "Control route in contract sibling", extraFile: "pkg/controlwire/extra.go", extraSource: `package controlwire; const bad = "/v1/auth/client/poll"`, want: `found pkg/controlwire/extra.go`},
		{name: "legacy route in exact contract", extraFile: controlContractFile, extraSource: `
const bad = "/v1/capacity"`, appendFile: true, want: `API path "/v1/capacity" must be named only in internal/api, found pkg/controlwire/auth.go`},
		{name: "Control route removed", remove: "/v1/auth/client/poll", want: `declared API path "/v1/auth/client/poll" was not found`},
		{name: "legacy route removed", remove: "/v1/waitlist", want: `declared API path "/v1/waitlist" was not found`},
		{name: "unknown deploy action", extraFile: "internal/api/extra.go", extraSource: `package api; func extra() { deployPath("x", "unexpected") }`, want: `undeclared per-deploy action "unexpected"`},
		{name: "missing deploy action", removeAction: "events", want: `declared per-deploy action "events" was not found`},
		{name: "extra upload site", extraFile: "internal/api/extra.go", extraSource: `package api; import "net/http"; var bad = http.MethodPut`, want: "2 files open a PUT"},
		{name: "second upload in same file", extraFile: uploadSite, extraSource: `
var anotherUpload = http.MethodPut`, appendFile: true, want: "opens 2 PUTs"},
		{name: "no upload", omitPut: true, want: "no upload PUT was found"},
		{name: "wrong upload site", omitPut: true, extraFile: "internal/api/extra.go", extraSource: `package api; import "net/http"; var bad = http.MethodPut`, want: "upload PUT found in internal/api/extra.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			init := exec.Command("git", "init", "--quiet")
			init.Dir = root
			init.Env = gitSafeEnv()
			if out, err := init.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
			write := func(name, data string) {
				t.Helper()
				path := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module guardfixture\n\ngo 1.26\n")
			var api, wire strings.Builder
			api.WriteString("package api\nfunc deployPath(...string) {}\nfunc calls(){\n")
			for _, action := range []string{"start", "publish", "events"} {
				if action != tc.removeAction {
					fmt.Fprintf(&api, "deployPath(\"x\",%q)\n", action)
				}
			}
			api.WriteString("}\n")
			wire.WriteString("package controlwire\n")
			for i, path := range legacy {
				if path != tc.remove {
					fmt.Fprintf(&api, "const legacy%d = %q\n", i, path)
				}
			}
			for i, path := range control {
				if path != tc.remove {
					fmt.Fprintf(&wire, "const control%d = %q\n", i, path)
				}
			}
			write("internal/api/client.go", api.String())
			write(controlContractFile, wire.String())
			if tc.omitPut {
				write(uploadSite, "package flow\n")
			} else {
				write(uploadSite, "package flow\nimport \"net/http\"\nvar upload = http.MethodPut\n")
			}
			if tc.extraFile != "" {
				if tc.appendFile {
					path := filepath.Join(root, filepath.FromSlash(tc.extraFile))
					b, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					write(tc.extraFile, string(b)+tc.extraSource+"\n")
				} else {
					write(tc.extraFile, tc.extraSource+"\n")
				}
			}
			// Every policy case must compile before a literal scan can grade it.
			compile := exec.Command("go", "test", "-run=^$", "./...")
			compile.Dir = root
			for _, item := range gitSafeEnv() {
				name, _, _ := strings.Cut(item, "=")
				if name != "GOFLAGS" && name != "GOWORK" && name != "GOPROXY" {
					compile.Env = append(compile.Env, item)
				}
			}
			compile.Env = append(compile.Env, "GOFLAGS=", "GOWORK=off", "GOPROXY=off")
			if out, err := compile.CombinedOutput(); err != nil {
				t.Fatalf("fixture compilation: %v: %s", err, out)
			}
			t.Log("fixture compiled")
			surface := readNetworkSurface(t, root)
			if surface.scanned < 3 {
				t.Fatalf("only %d source files discovered", surface.scanned)
			}
			findings := networkSurfaceFindings(surface)
			if tc.want == "" {
				if len(surface.paths) != 12 || len(findings) != 0 {
					t.Fatalf("positive: %d paths, findings %v", len(surface.paths), findings)
				}
				return
			}
			for _, finding := range findings {
				if strings.Contains(finding, tc.want) {
					return
				}
			}
			t.Fatalf("wanted %q; got %v", tc.want, findings)
		})
	}
}

// A parser failure must stop the guard rather than becoming a clean scan.
func TestNetworkSurfaceRejectsMalformedSource(t *testing.T) {
	if root := os.Getenv("CURIOUS_GUARD_MALFORMED_FIXTURE"); root != "" {
		readNetworkSurface(t, root)
		t.Fatal("malformed source was accepted")
		return
	}
	root := t.TempDir()
	init := exec.Command("git", "init", "--quiet")
	init.Dir = root
	init.Env = gitSafeEnv()
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.go"), []byte("package broken\nfunc ("), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNetworkSurfaceRejectsMalformedSource$", "-test.count=1")
	child.Env = gitSafeEnv("CURIOUS_GUARD_MALFORMED_FIXTURE=" + root)
	out, err := child.CombinedOutput()
	if err == nil || (!strings.Contains(string(out), "parsing ") || !strings.Contains(string(out), "bad.go")) {
		t.Fatalf("malformed source did not fail during discovery: %v: %s", err, out)
	}
}
