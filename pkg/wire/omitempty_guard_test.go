package wire

// An eighth gap, and the one that re-opened the first.
//
// TestZeroValueMarshalsEveryField compares a zero value's emitted key set
// against the fixture, which catches `omitempty` ADDED TO AN EXISTING
// field — that is the case its header describes. It does not catch a NEW
// field that carries `omitempty` from birth: the field vanishes at its
// zero value before the comparison, the fixture never mentions it, and
// the hand-written golden value does not populate it either. A line
// review demonstrated it (2026-08-17) and a mutation in this repo
// confirmed it: adding `Stream string \`json:"stream,omitempty"\`` to
// LogEvent, touching nothing else, left the whole suite green.
//
// That is worse than a missed field. `omitempty` is the spelling a Go
// author reaches for by reflex on anything optional, so the natural way
// to add a field is the one way that enters the frozen contract with no
// fixture pinning its name — and an unpinned field's later rename or
// removal is invisible too, which is the exact defect this package
// exists to prevent.
//
// Note what does NOT need a guard, because it already fails: a new field
// WITHOUT `omitempty` is emitted at its zero value, so compareKeys sees a
// key the fixture lacks and fails with "emitted but absent from the
// fixture". The hole is `omitempty` specifically, so banning it closes
// the gap structurally rather than by enumeration.
//
// A blanket ban is the right rule here rather than a judgement call per
// field: in an additive-only contract, a response that sometimes omits a
// key forces every client to distinguish "absent" from "zero", and
// `accounts_left: 0` is precisely the closed-capacity signal this package
// already documents as load-bearing. If a genuinely optional field is
// ever needed, the deliberate shapes are a pointer with a documented null
// meaning or a separate presence flag — both of which are visible in the
// fixture, which is the property being protected.

import (
	"go/ast"
	"strings"
	"testing"
)

func TestNoFieldUsesOmitempty(t *testing.T) {
	var offenders []string
	for _, f := range parseSources(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if field.Tag == nil || !strings.Contains(field.Tag.Value, "omitempty") {
					continue
				}
				name := "<embedded>"
				if len(field.Names) > 0 {
					name = field.Names[0].Name
				}
				offenders = append(offenders, name+" "+field.Tag.Value)
			}
			return true
		})
	}
	if len(offenders) > 0 {
		t.Errorf("omitempty is not permitted in this package: %v\n"+
			"A field that vanishes at its zero value is a breaking change for any client "+
			"reading it, and a NEW field carrying omitempty enters the contract with no "+
			"fixture pinning its name — so its later rename or removal is invisible too. "+
			"If the field is genuinely optional, use a pointer with a documented null "+
			"meaning, and give it a fixture.", offenders)
	}
}
