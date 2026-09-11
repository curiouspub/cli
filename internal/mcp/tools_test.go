package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// ---------------------------------------------------------------------
// The instruments
// ---------------------------------------------------------------------

// deployScript is a real HTTP server speaking the real wire contract, so
// every row here goes through the shipped client's own encoding and
// decoding rather than through a double that agrees with it by
// construction.
//
// IT RECORDS EVERY REQUEST IN ORDER, including ones to paths this
// contract does not define. "These tools drive the same sequence the
// command drives" is a claim about an ORDER of calls, and a recorder
// that only noted the ones it recognised could not tell a sequence that
// skipped a step from one that invented a step of its own.
type deployScript struct {
	mu    sync.Mutex
	calls []string

	// uploadPath is where this server hands out upload links. It is a
	// path on this same server so one handler sees the whole run.
	uploadPath string

	deployID  string
	subdomain string
	expiresAt time.Time

	// frames is the build log this fixture serves, written out in full.
	frames []string

	// refuseAuth answers both login endpoints with one error envelope,
	// so a row can drive the SERVER'S OWN SENTENCE into whatever this
	// program does with it. It is the only thing here that produces
	// foreign prose, which is why it exists rather than a row asserting
	// on a message this file wrote.
	refuseAuth *scriptedRefusal

	// capacityShut answers the capacity check with a closed door, which
	// is the only condition under which the gate's verdict is visible
	// from outside: open, a run that checks and a run that does not look
	// exactly the same.
	capacityShut bool
}

// scriptedRefusal is one error envelope, as the wire carries it.
type scriptedRefusal struct {
	status  int
	code    wire.ErrorCode
	message string
}

func (s *deployScript) note(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, event)
}

func (s *deployScript) ordered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *deployScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	// EVERY ARM MATCHES BY TAIL, never by a whole path. This package is
	// forbidden from spelling a route and a guard says so, because a
	// fixture is where a second copy of one lands first — and this arm
	// was the second copy, caught by that guard on the run after it was
	// written.
	case s.refuseAuth != nil && strings.Contains(path, "/auth/"):
		s.note("auth refused")
		w.Header().Set("Content-Type", "application/json")
		if wire.CarriesRetryAfter(s.refuseAuth.code) {
			// THE CONTRACT PROMISES A TIME FOR THESE CODES, so the
			// fixture sends one. A server that broke its own promise
			// would put this row's subject — what the client does with
			// the MESSAGE — behind a branch about a missing header.
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(s.refuseAuth.status)
		_ = json.NewEncoder(w).Encode(wire.ErrorResponse{Error: wire.Error{
			Code: s.refuseAuth.code, Message: s.refuseAuth.message,
		}})

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/capacity"):
		s.note("capacity")
		writeJSON(w, wire.CapacityResponse{
			Open:         !s.capacityShut,
			AccountsLeft: 200,
			ResetsAt:     fixedExpiry,
		})

	case r.Method == http.MethodPut && path == s.uploadPath:
		s.note("upload")
		w.WriteHeader(http.StatusOK)

	// MATCHED BY SUFFIX, NEVER BY THE WHOLE PATH. This package is
	// forbidden from spelling a route — every call it makes goes through
	// the package that owns the route table, and a guard says so — and a
	// fixture is where a second copy of a path would land first. Matching
	// the tail is enough to tell these five apart and spells none of
	// them.
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/deploys"):
		s.note("create")
		writeJSON(w, wire.DeployCreateResponse{
			DeployID:  s.deployID,
			UploadURL: "http://" + r.Host + s.uploadPath,
		})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		s.note("start")
		writeJSON(w, wire.DeployStartResponse{Status: wire.StatusBuilding})

	case r.Method == http.MethodGet && strings.HasSuffix(path, "/events"):
		s.note("stream")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range s.frames {
			_, _ = w.Write([]byte(frame))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/publish"):
		s.note("publish")
		writeJSON(w, wire.DeployPublishResponse{
			Subdomain: s.subdomain,
			ExpiresAt: s.expiresAt,
		})

	default:
		// RECORDED RATHER THAN SILENTLY ANSWERED. A request to a path
		// this contract does not define is a wiring defect, and answered
		// with a 404 it would arrive dressed as a routing result.
		s.note("UNEXPECTED " + r.Method + " " + path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// The build log frames these rows serve, built through the contract's
// own types so a fixture cannot describe a stream the server could not
// send.
func frame(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

func logLine(line string) string {
	data, err := json.Marshal(wire.LogEvent{Line: line})
	if err != nil {
		panic(err)
	}
	return frame(string(wire.EventLog), string(data))
}

func phaseAt(phase wire.Phase) string {
	data, err := json.Marshal(wire.PhaseEvent{Phase: phase})
	if err != nil {
		panic(err)
	}
	return frame(string(wire.EventPhase), string(data))
}

func finished(status wire.DeployStatus) string {
	data, err := json.Marshal(wire.DoneEvent{Status: status})
	if err != nil {
		panic(err)
	}
	return frame(string(wire.EventDone), string(data))
}

func diagnostic(code wire.ErrorCode, message string) string {
	data, err := json.Marshal(wire.Error{Code: code, Message: message})
	if err != nil {
		panic(err)
	}
	return frame(string(wire.EventError), string(data))
}

// fixedExpiry is the instant the scripted publish reports. It is fixed
// so a row asserting on the answer is asserting on the rendering rather
// than on when the suite happened to run.
var fixedExpiry = time.Date(2026, 9, 14, 9, 30, 0, 0, time.UTC)

// toolsRun is a server with the four tools registered, pointed at a
// scripted API, with a login already stored.
type toolsRun struct {
	t      *testing.T
	server *Server
	script *deployScript
}

// newToolsRun wires everything production wires, and nothing else.
//
// THERE IS NO SEAM HERE AND THAT IS DELIBERATE. The tools take no
// dependencies, so a row reaches them the way a client does: through the
// environment, the configuration file and the protocol. What that buys
// is that there is nowhere for production and this row to differ — a
// double supplied here could not tell a tool that drives the deploy
// sequence from one that drives a copy of it.
func newToolsRun(t *testing.T, script *deployScript) *toolsRun {
	t.Helper()

	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	// A CONFIG PATH AND AN ENDPOINT OF THIS ROW'S OWN. Without both, a
	// developer's real login and real endpoint decide whether a row
	// passes — and the first thing these tools do is read exactly those.
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
	t.Setenv("CURIOUS_API_URL", srv.URL)

	cfg, err := config.Load(srv.URL)
	if err != nil {
		t.Fatalf("loading a fresh config: %v", err)
	}
	if err := cfg.Save(ui.Secret("stored-token-never-printed"), srv.URL); err != nil {
		t.Fatalf("storing a token: %v", err)
	}

	s := testServer()
	RegisterTools(s)
	return &toolsRun{t: t, server: s, script: script}
}

// call runs one tool through the whole protocol — dispatch, containment
// and framing included — and hands back its result.
func (r *toolsRun) call(name, arguments string) Result {
	r.t.Helper()
	stdout := runCall(r.t, r.server, callMessage("1", name, arguments))
	return resultOf(r.t, onlyReply(r.t, stdout))
}

// text is a result's one block of text.
func text(t *testing.T, result Result) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("the result carries %d content blocks, want 1: %+v", len(result.Content), result)
	}
	return result.Content[0].Text
}

// decodeInto reads a successful result's answer, which is an object.
func decodeInto(t *testing.T, result Result, into any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("the call failed:\n%s", text(t, result))
	}
	if err := json.Unmarshal([]byte(text(t, result)), into); err != nil {
		t.Fatalf("the answer is not the object this tool documents (%v):\n%s",
			err, text(t, result))
	}
}

// project is one of the committed project directories.
func project(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "projects", name)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("project fixture %s: %v", name, err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolving %s: %v", root, err)
	}
	return abs
}

// ---------------------------------------------------------------------
// The surface
// ---------------------------------------------------------------------

// TestTheServedToolsAreExactlyTheFour, and the negative names the absent
// one rather than leaving it to be inferred.
//
// A ROW THAT ONLY CHECKED THE FOUR ARE PRESENT CANNOT SEE A FIFTH. The
// tool that must not be here is whoami: it would have to wrap a route
// the API does not serve, so every call to it would answer an agent with
// a transport failure — and the failure mode of listing it is a model
// that keeps trying. Naming it means this row cannot pass by never
// looking for it.
//
// THE LIST COMES OFF THE PROTOCOL rather than out of the registry, so it
// is what a client would actually be told. A registry a listing does not
// render is a tool nobody can call.
func TestTheServedToolsAreExactlyTheFour(t *testing.T) {
	s := testServer()
	RegisterTools(s)

	stdout := runCall(t, s, `{"jsonrpc":"2.0","id":"1","method":"tools/list"}`)
	var listed struct {
		Tools []toolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(onlyReply(t, stdout).Result, &listed); err != nil {
		t.Fatalf("decoding the tool listing: %v", err)
	}

	got := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
	}
	want := []string{toolLoginStart, toolLoginVerify, toolDeploySite, toolDeployStatus}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the served tools are %v, want exactly %v", got, want)
	}

	for _, absent := range []string{"whoami", "list_sites"} {
		for _, name := range got {
			if name == absent {
				t.Errorf("%s is registered. It would have to wrap a route the API does "+
					"not serve, so every call to it answers with a transport failure — "+
					"and a model that can see it will keep trying.", absent)
			}
		}
	}
}

// TestEveryToolAnswersItsOwnName is the acceptance half of the row
// above, and it is what stops that one passing against a registry of
// four names with nothing behind them.
//
// Each is called with no arguments at all, which every one of them
// refuses — three for a missing required field and the deploy for a
// project that is not there. THE REFUSAL IS THE EVIDENCE: it is the
// tool's own words, so it can only have come from that tool's handler,
// where an unknown name would have come back as a protocol error and a
// registry of empty shells would have come back as something generic.
func TestEveryToolAnswersItsOwnName(t *testing.T) {
	s := testServer()
	RegisterTools(s)
	// A config path of this row's own: one of these tools reads the
	// stored login before it refuses, and it must not read a real one.
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))

	for name, wantSays := range map[string]string{
		toolLoginStart:   "email is required",
		toolLoginVerify:  "email is required",
		toolDeployStatus: "deploy_id is required",
	} {
		t.Run(name, func(t *testing.T) {
			result := resultOf(t, onlyReply(t, runCall(t, s, callMessage("1", name, `{}`))))
			if !result.IsError {
				t.Fatalf("%s accepted a call with no arguments: %s", name, text(t, result))
			}
			if got := text(t, result); !strings.Contains(got, wantSays) {
				t.Errorf("%s answered %q, want it to name the field it needs (%q)",
					name, got, wantSays)
			}
		})
	}
}

// TestNoDescriptionCarriesANumberThatIsNotAWireConstant.
//
// A description is what a model reads to decide how to call a tool, and
// a figure in one is a promise. The only figures this client may make are
// the contract's own, because those are the numbers the server enforces —
// everything else is server POLICY, which changes without a release and
// which this binary is not told about. A quota or an expiry typed here
// would read as authoritative and go stale in silence, and a description
// stating a limit the server does not enforce is worse than one stating
// none.
//
// THE EXPECTATION IS DERIVED FROM THE CONTRACT, not compared against
// literals. A row holding its own copy of the six would agree with a
// description holding a seventh, because neither side would be reading
// the thing that decides.
//
// THE UNIVERSE IS EVERY DESCRIPTION A CLIENT RECEIVES: the tool's own,
// and every description inside its input schema. A model reads both, so
// a figure is exactly as authoritative in either — and scoping this to
// the first would leave the likelier home for a number outside the row.
//
// REQUIRED MUTATION, run 2026-09-11: add "Each account gets 5 deploys a
// day" to a description. Reds naming 5. Second, run the same day: add
// "sites expire after 72 hours". Reds naming 72. Both are the drift this
// exists to catch, and neither is a number the contract defines.
func TestNoDescriptionCarriesANumberThatIsNotAWireConstant(t *testing.T) {
	// THE SET IS OF VALUES AND NOT OF CONSTANTS, and the difference is
	// measured rather than tidy: three of the six are the same number,
	// because the packed cap, the source total and the output total are
	// deliberately equal. Nothing reading a rendered description can tell
	// which of the three a stated figure came from, so this set is keyed
	// by the value and each entry names every constant that carries it.
	// A row whose message named one of them would be claiming more than
	// it can check — which was found by a mutation that dropped one of
	// the three and stayed green, and is recorded here rather than fixed
	// by a cleverer matcher, because there is nothing in the rendered
	// text to be cleverer about.
	permitted := map[string][]string{}
	for _, constant := range []struct {
		name  string
		value int
	}{
		{"MaxPackedBytes", wire.MaxPackedBytes},
		{"MaxSourceFiles", wire.MaxSourceFiles},
		{"MaxSourceFileBytes", wire.MaxSourceFileBytes},
		{"MaxSourceTotalBytes", wire.MaxSourceTotalBytes},
		{"MaxOutputFiles", wire.MaxOutputFiles},
		{"MaxOutputTotalBytes", wire.MaxOutputTotalBytes},
	} {
		rendered := fmt.Sprint(constant.value)
		permitted[rendered] = append(permitted[rendered], constant.name)
	}

	s := testServer()
	RegisterTools(s)
	listed := s.listTools()
	if len(listed.Tools) == 0 {
		t.Fatal("no tool was listed, so this row measured nothing")
	}

	scanned := 0
	for _, tool := range listed.Tools {
		for _, described := range describedStrings(t, tool) {
			scanned++
			for _, number := range numbersIn(described.text) {
				if _, allowed := permitted[number]; allowed {
					continue
				}
				t.Errorf("%s's %s carries the number %s, which is not one of the wire "+
					"contract's constants.\nA description may state a figure only when the "+
					"figure is one the server takes from the contract. Quota and expiry are "+
					"server policy: say that the server reports them, and name no number.\n"+
					"The text: %q", tool.Name, described.where, number, described.text)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no description was scanned, so this row cannot tell a clean surface " +
			"from a walk that stopped matching")
	}

	// THE PERMITTED SET IS PROVED REACHABLE, or this row is satisfied by
	// descriptions that state no limits at all — which is a surface that
	// has stopped telling an agent what it may send, and looks identical
	// from here.
	//
	// IT COMPARES WHOLE DIGIT RUNS AND NOT SUBSTRINGS, which is not a
	// refinement: written as a substring search this half was GREEN
	// against a description that had dropped the file-count limit
	// entirely, because that number is a prefix of the byte cap beside
	// it. A search for 3000 inside 30000000 finds it every time. The
	// runs are the same ones the refusal half reads, so the two cannot
	// disagree about where a number starts and stops.
	stated := map[string]bool{}
	for _, described := range everyDescription(t, listed.Tools) {
		for _, number := range numbersIn(described) {
			stated[number] = true
		}
	}
	for number, names := range permitted {
		if !stated[number] {
			sort.Strings(names)
			t.Errorf("no description states %s, which is the value of %s. The limits are "+
				"what let an agent self-serve a refusal instead of retrying blindly, and a "+
				"row that never sees one of them cannot tell a stated limit from a missing "+
				"one", number, strings.Join(names, " and "))
		}
	}
}

// describedText is one description a client receives, and where it came
// from.
type describedText struct {
	where string
	text  string
}

// describedStrings is every description one tool puts in front of a
// model: its own, and every one inside its input schema.
//
// THE SCHEMA IS WALKED RATHER THAN PATTERN-MATCHED. A description can
// sit at any depth — a property, a nested object, an array's items — and
// a scan that read only the top level would report clean over a figure
// one level down.
func describedStrings(t *testing.T, tool toolDescriptor) []describedText {
	t.Helper()
	out := []describedText{{where: "description", text: tool.Description}}

	var schema any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("%s's input schema is not valid JSON: %v", tool.Name, err)
	}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch v := node.(type) {
		case map[string]any:
			for key, value := range v {
				if key == "description" {
					if s, isString := value.(string); isString {
						out = append(out, describedText{where: "schema at " + path, text: s})
						continue
					}
				}
				walk(value, path+"/"+key)
			}
		case []any:
			for i, value := range v {
				walk(value, fmt.Sprintf("%s/%d", path, i))
			}
		}
	}
	walk(schema, "")
	return out
}

// everyDescription is the flattened text of every description across a
// listing, for the reachability half above.
func everyDescription(t *testing.T, tools []toolDescriptor) []string {
	t.Helper()
	var out []string
	for _, tool := range tools {
		for _, described := range describedStrings(t, tool) {
			out = append(out, described.text)
		}
	}
	return out
}

// numbersIn is every run of digits in a string.
//
// IT READS DIGIT RUNS RATHER THAN "NUMBERS", which is what makes the
// rule enforceable: a figure written with separators — three thousand
// spelled with a comma — would arrive as two short runs that match no
// constant, so the only way to state a limit here is to state the
// contract's own value exactly as the contract holds it. A figure spelled
// in WORDS passes, and that is deliberate rather than a hole: "six
// digits" is a sentence about shape, and what this forbids is a NUMBER
// presented as a limit somebody could act on.
func numbersIn(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		digit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		switch {
		case digit && start < 0:
			start = i
		case !digit && start >= 0:
			out = append(out, s[start:i])
			start = -1
		}
	}
	return out
}

// ---------------------------------------------------------------------
// deploy_site
// ---------------------------------------------------------------------

// TestDeploySiteDrivesTheSequenceTheCommandDrives is the row the whole
// split exists for.
//
// THE CALL LOG IS THE SERVER'S, in order, over a real HTTP connection —
// so what it records is what left this process rather than what a double
// was asked to do. A second implementation of the deploy path would have
// to reproduce that order exactly, including the upload going to the link
// the create handed out and the publish coming last, and the cheapest
// way to be sure nobody wrote one is that this order is produced by the
// sequence itself.
//
// THE WARNINGS ARE HERE TOO, in one row rather than two, because they
// are a property OF THIS RUN: the project has hard-coded development
// URLs, a terminal would stop and ask about them, and this surface must
// carry them back instead. Split across two rows each would need its own
// deploy, and the second would be measuring the same sequence twice.
//
// STDIN IS A PIPE THAT NOBODY WRITES TO, and the write end is left OPEN.
// That is what makes the no-prompt claim real: a build that decided it
// could ask would block on a read nobody will answer, and the deadline
// turns that hang into a named failure rather than a suite that never
// finishes. Closing it would produce an end-of-input instead, and the
// row would be measuring a cancellation.
//
// REQUIRED MUTATION, run 2026-09-11: make the agent prompter's Confirm
// return the not-interactive sentinel instead of yes. Reds here — the
// deploy comes back as an error naming the two login tools, because the
// sentinel is what a run with nobody to ask produces.
func TestDeploySiteDrivesTheSequenceTheCommandDrives(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe for stdin: %v", err)
	}
	t.Cleanup(func() { _ = stdinR.Close(); _ = stdinW.Close() })
	realStdin := os.Stdin
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = realStdin })

	script := &deployScript{
		uploadPath: "/object-store/put",
		deployID:   "dpl-ordered",
		subdomain:  "quick-koala-4f2a",
		expiresAt:  fixedExpiry,
		frames: []string{
			phaseAt(wire.PhaseInstalling),
			logLine("added 41 packages"),
			finished(wire.StatusBuilt),
		},
	}
	run := newToolsRun(t, script)

	result := run.call(toolDeploySite,
		fmt.Sprintf(`{"dir":%q}`, project(t, "localhost-hits")))

	var answer deploySiteResult
	decodeInto(t, result, &answer)

	if got, want := script.ordered(), []string{"create", "upload", "start", "stream", "publish"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the run made these calls: %v\nwant exactly the sequence the command "+
			"makes: %v", got, want)
	}

	if answer.DeployID != script.deployID {
		t.Errorf("deploy_id = %q, want the one the create returned (%q)",
			answer.DeployID, script.deployID)
	}
	if want := flow.PublishedURL(script.subdomain); answer.URL != want {
		t.Errorf("url = %q, want the address the sequence composes from the label (%q)",
			answer.URL, want)
	}
	if want := fixedExpiry.Format(time.RFC3339); answer.ExpiresAt != want {
		t.Errorf("expires_at = %q, want the instant the publish reported (%q)",
			answer.ExpiresAt, want)
	}

	// THE WARNING IS PRESENT AS TEXT, not merely counted. A row that
	// asserted a finding arrived would pass against a surface that
	// carried the severity and dropped the sentence — which is the half
	// that says which file to open.
	var carried []string
	for _, finding := range answer.Findings {
		if finding.Severity == string(check.SeverityWarning) {
			carried = append(carried, finding.Message)
		}
	}
	if len(carried) == 0 {
		t.Fatalf("the result carried no warning, and this project has two:\n%+v",
			answer.Findings)
	}
	joined := strings.Join(carried, "\n")
	for _, want := range []string{"src/pages/index.astro", "src/lib/api.ts", "localhost"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warnings do not mention %q:\n%s", want, joined)
		}
	}
}

// TestTheAddressIsBuiltFromTheLabelTheServerSent.
//
// A row driving one label cannot tell an address composed from it apart
// from one written out by hand, because a hardcoded string is right
// exactly once and that is the run it was copied from. Two labels, two
// addresses, and the pair is the assertion.
//
// The expectation comes from the composer rather than from a literal, so
// this row spells no domain — the domain has one home, and a second copy
// in a test is a second copy.
func TestTheAddressIsBuiltFromTheLabelTheServerSent(t *testing.T) {
	for _, label := range []string{"quick-koala-4f2a", "brave-otter-91bd"} {
		t.Run(label, func(t *testing.T) {
			script := &deployScript{
				uploadPath: "/object-store/put",
				deployID:   "dpl-" + label,
				subdomain:  label,
				frames:     []string{finished(wire.StatusBuilt)},
			}
			run := newToolsRun(t, script)

			var answer deploySiteResult
			decodeInto(t, run.call(toolDeploySite,
				fmt.Sprintf(`{"dir":%q}`, project(t, "valid"))), &answer)

			if want := flow.PublishedURL(label); answer.URL != want {
				t.Errorf("url = %q, want %q", answer.URL, want)
			}
			if !strings.Contains(answer.URL, label) {
				t.Errorf("url = %q and does not contain the label the server sent (%q), "+
					"so it cannot have been built from it", answer.URL, label)
			}
		})
	}
}

// TestADeploySiteCallIsWatchedTheWayAClientAsks.
//
// The notification machinery has its own rows next door, driven by a
// tool written for them. This is the join: that the real deploy tool
// reports through the sink it is handed, with the build's own phases and
// output in the notifications — which is a different claim from "the
// sink works", and the one a client depends on.
func TestADeploySiteCallIsWatchedTheWayAClientAsks(t *testing.T) {
	script := &deployScript{
		uploadPath: "/object-store/put",
		deployID:   "dpl-watched",
		subdomain:  "quick-koala-4f2a",
		frames: []string{
			phaseAt(wire.PhaseInstalling),
			logLine("added 41 packages"),
			phaseAt(wire.PhaseBuilding),
			finished(wire.StatusBuilt),
		},
	}
	run := newToolsRun(t, script)

	stdout := runCall(t, run.server, watchedCall("1", toolDeploySite,
		fmt.Sprintf(`{"dir":%q}`, project(t, "valid")), `"watch-me"`))

	got := progressMessages(t, stdout)
	want := []string{
		"installing",
		"installing: added 41 packages",
		"building: added 41 packages",
	}
	if len(got) != len(want) {
		t.Fatalf("the client was sent %d notifications, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Message != want[i] {
			t.Errorf("notification %d says %q, want %q", i, got[i].Message, want[i])
		}
	}
}

// ---------------------------------------------------------------------
// deploy_status
// ---------------------------------------------------------------------

// TestDeployStatusNeverReportsAStatusTheStreamDidNotSay.
//
// TWO FIXTURES, BECAUSE ONE OF THEM CANNOT FAIL. A reader that always
// answered "not yet reported" satisfies the short stream perfectly, and
// one that always answered the same status satisfies the finished one —
// only the pair distinguishes a tool that is reading from one that has an
// opinion.
//
// The dangerous wrong answer is the last PHASE promoted to a status:
// "building" is a member of both vocabularies, so an agent switching on
// it would be right until the day it was not. Both rows assert the phase
// is carried in its own field and that the status is not it.
//
// REQUIRED MUTATION, run 2026-09-11: report the last phase as the status
// when the stream did not reach its terminal event. Reds the unfinished
// row on both the status and the reported flag.
func TestDeployStatusNeverReportsAStatusTheStreamDidNotSay(t *testing.T) {
	t.Run("a stream that never finished", func(t *testing.T) {
		script := &deployScript{
			uploadPath: "/object-store/put",
			frames: []string{
				phaseAt(wire.PhaseBuilding),
				logLine("astro building"),
			},
		}
		run := newToolsRun(t, script)

		var answer deployStatusResult
		decodeInto(t, run.call(toolDeployStatus, `{"deploy_id":"dpl-unfinished"}`), &answer)

		if answer.Reported {
			t.Error("reported is true for a stream that never reached its terminal event")
		}
		if answer.Status != notYetReported {
			t.Errorf("status = %q, want %q", answer.Status, notYetReported)
		}
		if answer.Phase != string(wire.PhaseBuilding) {
			t.Errorf("phase = %q, want the last one the stream named (%q)",
				answer.Phase, wire.PhaseBuilding)
		}
		if strings.Join(answer.Log, "\n") != "astro building" {
			t.Errorf("log = %q, want the line the stream sent", answer.Log)
		}
		if answer.DeployID != "dpl-unfinished" {
			t.Errorf("deploy_id = %q, want the one that was asked about", answer.DeployID)
		}
	})

	t.Run("a stream that finished", func(t *testing.T) {
		script := &deployScript{
			uploadPath: "/object-store/put",
			frames: []string{
				phaseAt(wire.PhaseUploading),
				logLine("astro build finished"),
				diagnostic(wire.CodeInternal, "one thing went wrong on the way"),
				finished(wire.StatusFailed),
			},
		}
		run := newToolsRun(t, script)

		var answer deployStatusResult
		decodeInto(t, run.call(toolDeployStatus, `{"deploy_id":"dpl-finished"}`), &answer)

		if !answer.Reported {
			t.Error("reported is false for a stream that carried its terminal event")
		}
		if answer.Status != string(wire.StatusFailed) {
			t.Errorf("status = %q, want the one the terminal event carried (%q)",
				answer.Status, wire.StatusFailed)
		}
		if answer.Phase != string(wire.PhaseUploading) {
			t.Errorf("phase = %q, want the last one the stream named", answer.Phase)
		}
		// A DIAGNOSTIC EXPLAINS AND NEVER ENDS, so it arrives beside the
		// status rather than in place of it.
		if len(answer.Errors) != 1 ||
			answer.Errors[0].Message != "one thing went wrong on the way" {
			t.Errorf("errors = %+v, want the one diagnostic the stream carried", answer.Errors)
		}
	})

	// THE THIRD STATE, and it was argued in a comment for a day before it
	// was asserted. status.go says the sentinel is chosen by whether the
	// stream SPOKE and never by whether the status happens to be a value
	// this build knows — "a terminal event carrying a status this build
	// has never heard of is still the stream reporting one, and rendering
	// the sentinel for it would be this client saying the deploy had not
	// finished when it had".
	//
	// Nothing tested it. Every fixture in this package and in the
	// sequence package sent StatusBuilt or StatusFailed, so both
	// branches a reader can see were covered and the one the comment
	// defends was not. Found 2026-09-12 by a mutation that made the
	// sentinel depend on the VALUE and left the package green.
	//
	// TWO CASES, because the drift has two shapes and one fixture only
	// catches one. An unmapped value is what an additive contract
	// actually produces — a status minted after this build shipped. An
	// empty one is the degenerate form, and it is the shape a nil-ish
	// check reaches for.
	//
	// REQUIRED MUTATION, run 2026-09-12: make reportedStatus return the
	// sentinel when the status is empty, or when it is not one of the
	// values this build knows. Reds here on the matching case, and on no
	// other row in either package.
	for _, tc := range []struct {
		name   string
		status wire.DeployStatus
	}{
		{name: "a status minted after this build shipped", status: "resurrected"},
		{name: "a terminal event carrying no status at all", status: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := &deployScript{
				uploadPath: "/object-store/put",
				frames: []string{
					phaseAt(wire.PhaseUploading),
					finished(tc.status),
				},
			}
			run := newToolsRun(t, script)

			var answer deployStatusResult
			decodeInto(t, run.call(toolDeployStatus, `{"deploy_id":"dpl-odd"}`), &answer)

			if !answer.Reported {
				t.Error("reported is false for a stream that carried a terminal " +
					"event — the stream spoke, whatever it said")
			}
			// THE ASSERTION THE COMMENT WAS MAKING. Reporting the
			// sentinel here would be this client telling an agent the
			// deploy has not finished, about a deploy that HAS.
			if answer.Status == notYetReported {
				t.Errorf("status = %q for a terminal event carrying %q. The sentinel "+
					"says the stream has not reported, and it has: an agent reading "+
					"this would wait for an answer that already arrived",
					answer.Status, tc.status)
			}
			if answer.Status != string(tc.status) {
				t.Errorf("status = %q, want %q — reported as received and unmapped, "+
					"because this build does not get to decide which statuses the "+
					"contract may grow", answer.Status, tc.status)
			}
		})
	}
}

// TestAToolThatNeedsALoginSaysWhichCallsMakeOne.
//
// The FACT — this machine holds no usable credential — belongs to the
// sequence package, which decides what "usable" means. The ACTION is
// this surface's, and it is the whole value of the message: a person is
// told to run a command in a terminal, and an agent has two tools to
// call. A refusal that named neither would leave a model guessing, and
// the guess is usually to try the same call again.
func TestAToolThatNeedsALoginSaysWhichCallsMakeOne(t *testing.T) {
	script := &deployScript{uploadPath: "/object-store/put"}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	// A configuration with NO token in it, which is a first run.
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
	t.Setenv("CURIOUS_API_URL", srv.URL)

	s := testServer()
	RegisterTools(s)
	result := resultOf(t, onlyReply(t, runCall(t, s,
		callMessage("1", toolDeployStatus, `{"deploy_id":"dpl-whatever"}`))))

	if !result.IsError {
		t.Fatalf("a tool needing a login answered without one: %s", text(t, result))
	}
	said := text(t, result)
	for _, want := range []string{toolLoginStart, toolLoginVerify} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal does not name %s:\n%s", want, said)
		}
	}
	if len(script.ordered()) != 0 {
		t.Errorf("a tool with no login still called %v", script.ordered())
	}
}

// TestARefusalCarriesWhyThereIsNoUsableLogin is the OTHER half of the
// row above, and it is a separate one because the two cases differ in
// the thing being asserted.
//
// A first run has no explanation to give and the refusal is right to
// offer none. A token that exists and cannot be used HAS one — it was
// issued against somewhere else, the file is corrupt, the file cannot be
// read — and that half is the only actionable thing in the message: "log
// in again" reads very differently once you know the login you already
// did was for a different server.
//
// IT IS THE ONE PATH THAT WAS COVERED BY NOTHING. The reason used to be
// recovered by trimming a prefix off a formatted message, which works
// until somebody rewords the sentinel it was formatted with — and the
// row above cannot see that, because a first run produces no reason to
// lose. Found by asking what this row would have to look like, rather
// than by anything going red.
//
// REQUIRED MUTATION, run 2026-09-11: drop the reason from the refusal —
// return the bare headline from noLoginText. Reds here, and the row
// above stays green, which is the asymmetry that makes them two.
func TestARefusalCarriesWhyThereIsNoUsableLogin(t *testing.T) {
	script := &deployScript{uploadPath: "/object-store/put"}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	// A TOKEN ISSUED SOMEWHERE ELSE. It is stored against a loopback
	// address the client would happily dial, so the refusal has to come
	// from the token belonging to another endpoint rather than from this
	// one being unusable.
	elsewhere := "http://127.0.0.1:1"
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
	cfg, err := config.Load(elsewhere)
	if err != nil {
		t.Fatalf("loading a fresh config: %v", err)
	}
	if err := cfg.Save(ui.Secret("token-for-another-server"), elsewhere); err != nil {
		t.Fatalf("storing a token: %v", err)
	}
	t.Setenv("CURIOUS_API_URL", srv.URL)

	s := testServer()
	RegisterTools(s)
	result := resultOf(t, onlyReply(t, runCall(t, s,
		callMessage("1", toolDeployStatus, `{"deploy_id":"dpl-whatever"}`))))

	if !result.IsError {
		t.Fatalf("a token issued elsewhere was accepted: %s", text(t, result))
	}
	said := text(t, result)
	if !strings.Contains(said, elsewhere) {
		t.Errorf("the refusal does not say where the stored login belongs, which is the "+
			"only half of it a reader can act on:\n%s", said)
	}
}

// TestALoginThatCostsNoCapacityIsNotRefusedByTheCapacityGate.
//
// The daily cap counts NEW accounts, and a repeat verify for an identity
// that already holds a token reissues rather than spending a second slot
// — so a run on a machine that is already logged in costs the day
// nothing, and refusing it would be refusing free work. The command
// enforces that by only reaching its login when the stored token is
// missing or refused; this tool has no such precondition, because an
// agent may call it whenever it likes, so it has to ask the same
// question itself.
//
// THE FIXTURE SHUTS THE DOOR, which is what makes the two cases
// distinguishable at all: with capacity open both would pass, and the
// row would be measuring nothing.
//
// REQUIRED MUTATION, run 2026-09-11: pass the gate HaveToken false
// unconditionally. Reds the logged-in half — the call is refused with
// the closed-door copy — and the other half stays green, which is the
// asymmetry that makes this one row rather than two.
func TestALoginThatCostsNoCapacityIsNotRefusedByTheCapacityGate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		storeAToken bool
		wantRefused bool
	}{
		{name: "already logged in", storeAToken: true, wantRefused: false},
		{name: "no login at all", storeAToken: false, wantRefused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := &deployScript{uploadPath: "/object-store/put", capacityShut: true}
			srv := httptest.NewServer(script)
			t.Cleanup(srv.Close)
			t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
			t.Setenv("CURIOUS_API_URL", srv.URL)

			if tc.storeAToken {
				cfg, err := config.Load(srv.URL)
				if err != nil {
					t.Fatalf("loading a fresh config: %v", err)
				}
				if err := cfg.Save(ui.Secret("stored-token-never-printed"), srv.URL); err != nil {
					t.Fatalf("storing a token: %v", err)
				}
			}

			s := testServer()
			RegisterTools(s)
			result := resultOf(t, onlyReply(t, runCall(t, s,
				callMessage("1", toolLoginStart, `{"email":"someone@example.com"}`))))

			if result.IsError != tc.wantRefused {
				t.Fatalf("refused=%v, want %v:\n%s", result.IsError, tc.wantRefused,
					text(t, result))
			}
			// THE GATE'S CALL IS ASSERTED, not just its verdict. A tool
			// that skipped the check entirely would pass the logged-in
			// half for the wrong reason, and the two are
			// indistinguishable from the result alone.
			asked := false
			for _, call := range script.ordered() {
				if call == "capacity" {
					asked = true
				}
			}
			if asked == tc.storeAToken {
				t.Errorf("the capacity check was %s on a run that %s a login, and the "+
					"gate is meant to run exactly when a login would spend one",
					map[bool]string{true: "made", false: "skipped"}[asked],
					map[bool]string{true: "already holds", false: "holds no"}[tc.storeAToken])
			}
		})
	}
}

// TestEveryTerminalSentinelRendersSomethingWrittenForAReader.
//
// A gap in the code and a gap in its tests have the same shape, because
// one imagination writes both — so the way to find one is to line the
// exported set up against the handled set rather than to reason about
// behaviour. The set is READ FROM THE PACKAGE THAT DECLARES IT, so a
// sentinel added there without a case here reds on the day it is added,
// whether or not anybody remembers this row exists.
//
// WHAT IT ASSERTS IS THAT SOMETHING WAS WRITTEN, not what. Each of these
// has an action that belongs to this surface, and a sentinel falling
// through renders its own Go text — a sentence written for a log, in
// which "no terminal to ask on" is what a model would be told to act on.
func TestEveryTerminalSentinelRendersSomethingWrittenForAReader(t *testing.T) {
	sentinels := exportedSentinels(t)
	if len(sentinels) == 0 {
		t.Fatal("no sentinel was found in the terminal package, so this row measured nothing")
	}
	for name, err := range sentinels {
		got := refusalText(err)
		if got == err.Error() {
			t.Errorf("%s renders as its own text (%q).\nThat sentence is written for a "+
				"log. Every one of these has an action that belongs to this surface — "+
				"which tool to call, what to do next — and a sentinel with no case here "+
				"tells a model nothing it can act on.", name, got)
		}
		if got == "" {
			t.Errorf("%s renders as nothing at all", name)
		}
	}
}
