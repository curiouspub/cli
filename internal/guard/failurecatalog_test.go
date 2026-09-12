package guard

import (
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

func TestActiveFailureIDsAreUniqueAndReachable(t *testing.T) {
	root := moduleRoot(t)
	active := make([]string, 0, len(ui.ActiveFailureIDs))
	for _, id := range ui.ActiveFailureIDs {
		active = append(active, string(id))
	}

	fset := token.NewFileSet()
	var files []parsedFile
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, parsedFile{path: path, file: f, src: src})
	}
	attachFailureTypeInfo(t, root, fset, files)

	used := map[string]bool{}
	for _, p := range files {
		if p.info == nil || filepath.Clean(p.path) == filepath.Join(root, "internal", "ui", "failureid.go") {
			continue
		}
		for _, obj := range p.info.Uses {
			c, ok := obj.(*types.Const)
			if !ok || c.Val().Kind() != constant.String {
				continue
			}
			typeKey := namedTypeKey(c.Type())
			if typeKey == modulePath+"/internal/ui.FailureID" ||
				typeKey == modulePath+"/internal/check.FailureFamily" {
				used[constant.StringVal(c.Val())] = true
			}
		}
	}
	for _, problem := range catalogProblems(active, used) {
		t.Error(problem)
	}
}

func namedTypeKey(typ types.Type) string {
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok {
		return ""
	}
	return objectKey(named.Obj())
}

func catalogProblems(active []string, used map[string]bool) []string {
	counts := map[string]int{}
	for _, id := range active {
		counts[id]++
	}
	var problems []string
	for id, count := range counts {
		if id == "" {
			problems = append(problems, "ActiveFailureIDs contains an empty id")
		}
		if count > 1 {
			problems = append(problems, "ActiveFailureIDs contains duplicate "+id)
		}
		if !used[id] {
			problems = append(problems, "ActiveFailureIDs contains orphan "+id)
		}
	}
	for id := range used {
		if counts[id] == 0 {
			problems = append(problems, "production uses id absent from ActiveFailureIDs: "+id)
		}
	}
	return problems
}

func TestCatalogSetValidationRejectsMissingDuplicatesAndOrphans(t *testing.T) {
	if activeFailureID("not-in-the-catalog") {
		t.Fatal("a non-empty id outside ActiveFailureIDs passed membership")
	}
	if !activeFailureID(string(ui.IDInternalFault)) {
		t.Fatal("a declared active id failed membership")
	}
	problems := catalogProblems([]string{"used", "used", "orphan"}, map[string]bool{
		"used": true, "unknown": true,
	})
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"duplicate used", "orphan orphan", "absent from ActiveFailureIDs: unknown"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q from catalog failures:\n%s", want, joined)
		}
	}
}
