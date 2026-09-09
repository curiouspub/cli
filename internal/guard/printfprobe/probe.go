//go:build printfprobe

// Package printfprobe is never built by an ordinary command, never
// tested and never shipped: one build tag keeps it out of `./...`
// entirely. It exists so a row can ask a question no absence check can
// answer — whether go vet still RECOGNISES this project's rendering
// methods as printf wrappers.
//
// The guard beside it asserts that `go vet ./...` is clean. That is
// worth exactly nothing if the analyser has quietly stopped looking at
// the two methods it is about, which is a single refactor away: the
// detection depends on Step and Result forwarding their own format and
// variadic to a print call, and the obvious tidying of that code removes
// it without any test going red. So this package writes the defect on
// purpose, and the row requires vet to report it.
package printfprobe

import "github.com/curiouspub/cli/internal/ui"

// SomebodyElsesTextAsTheFormat is the seam: a caller handing a server's
// sentence to the methods that treat their first argument as this
// program's own prose. Both lines must be reported.
func SomebodyElsesTextAsTheFormat(u *ui.UI, fromTheServer string) {
	u.Step(fromTheServer)
	u.Result(fromTheServer)
}
