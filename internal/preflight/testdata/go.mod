// This directory is EXCLUDED FROM THE PUBLISHED MODULE, and that is the
// whole purpose of this file.
//
// A module zip's file paths must be ASCII, and two fixtures here are
// named for what they test: a directory whose name is a single astral
// character, and one whose name carries a Latin-1 accent. Both are the
// SUBJECT of a pre-flight row — a config file naming its source
// directory through an escape has to resolve to a real directory on
// disk, and only a real directory with that real name proves it does.
//
// With those paths inside the module, `go mod download` refuses the
// whole thing:
//
//	create zip: internal/preflight/testdata/…/<astral>/pages/.gitkeep:
//	malformed file path: invalid char
//
// which is not a warning. It makes this module UNFETCHABLE at any commit
// containing them — measured through the proxy and directly, both
// identical — so every consumer is pinned to whatever version predates
// the fixtures, and no wire change since can be consumed by anything.
//
// A go.mod here makes this subtree a separate module, which the parent's
// zip omits. Nothing imports it, nothing builds it, and `go test ./...`
// in the repository root does not descend into it — the go command has
// always ignored `testdata` for package loading. The fixtures stay
// exactly as they are, which matters: renaming them to ASCII would mean
// the rows that need those names no longer test what they are for.
//
// The guard beside this file is what keeps the property: it refuses any
// tracked path outside a nested module that a module zip would reject.
module curiouspub.example/preflight-testdata

go 1.24
