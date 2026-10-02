package preflight

import (
	"fmt"
	"path/filepath"

	"github.com/curiouspub/cli/internal/check"
)

// DefaultPublicDir is where Astro looks for the files it copies into the
// built site unchanged, when the config does not say otherwise.
const DefaultPublicDir = "public"

// PublicDir is the folder whose files keep their own names in the built
// site, and what the read that found it has to say.
//
// IT EXISTS BECAUSE THE FILE-NAME CHECK HAD BEEN ASKING THE WRONG TREE.
// The platform's rule about which characters a name may carry is a rule
// about the files it stores and serves, which are the build's OUTPUT.
// Astro copies the public folder into that output byte for byte, so a
// name there is a name the platform will be asked to store. Everything
// else is compiled: a page called `[slug].astro` is Astro's documented
// way of writing a dynamic route, and it never reaches the output under
// that name. Checking every source file refused those projects outright,
// and a starter template with a blog was enough to meet it.
type PublicDir struct {
	// Path is project-relative and slash-separated. "." means the
	// project root itself.
	Path string

	// Results carries the warning when the configured folder could not
	// be read and Path is the default instead. It claims no check: the
	// file-name check owns the id, and this is that check saying where
	// it looked.
	Results check.Results
}

// ResolvePublicDir reads publicDir out of the project's astro.config,
// with public/ as the default.
//
// A VALUE IT CANNOT READ FALLS BACK TO THE DEFAULT AND SAYS SO, and never
// refuses. Not knowing where the public folder is costs, at worst, a
// name refused after the build rather than before it; refusing the
// deploy over it would make a working project undeployable because of
// how its config is written.
//
// AND IT SAYS SO ONLY WHEN THE CONFIG NAMES publicDir. A config whose
// scan gave up for another reason — a template literal, a spread — and
// never mentions the key gets the default in silence, because that is
// what Astro uses for it. Warning there would repeat a measured mistake:
// a check that said "I could not confirm this" on most real configs,
// where nothing was wrong. What that leaves silent is a publicDir set
// inside a spread from another file, which a scan of this one file
// cannot see by construction.
func ResolvePublicDir(fsys FS, root string) PublicDir {
	configPath, _, unchecked, found := findConfig(fsys, root)
	if !found {
		if len(unchecked) > 0 {
			return fallBack(fmt.Sprintf("%s couldn't be checked", joinWithAnd(unchecked)))
		}
		return PublicDir{Path: DefaultPublicDir}
	}
	configName := filepath.Base(configPath)

	content, err := readConfigCapped(fsys, configPath)
	if err != nil {
		return fallBack(fmt.Sprintf("%s couldn't be read (%v)", configName, err))
	}

	parsed := parseAstroConfig(content)
	switch {
	case parsed.unresolved:
		if !parsed.publicDirMentioned {
			return PublicDir{Path: DefaultPublicDir}
		}
		return fallBack(fmt.Sprintf("%s names publicDir, but it also uses %s, so its value "+
			"couldn't be read", configName, parsed.unresolvedReason))
	case !parsed.publicDirFound:
		if !parsed.publicDirMentioned {
			return PublicDir{Path: DefaultPublicDir}
		}
		return fallBack(fmt.Sprintf("%s names publicDir in a way this check doesn't read",
			configName))
	case parsed.publicDirAmbiguous:
		return fallBack(fmt.Sprintf("%s sets publicDir more than once", configName))
	case !parsed.publicDirResolved:
		return fallBack(fmt.Sprintf("%s sets publicDir to a value worked out when the "+
			"config runs, which this check doesn't run", configName))
	}

	resolved, rejectReason := resolveSrcDirPath(parsed.publicDirValue)
	if rejectReason != "" {
		return fallBack(fmt.Sprintf("%s sets publicDir to %q, which %s", configName,
			parsed.publicDirValue, rejectReason))
	}
	return PublicDir{Path: resolved}
}

// fallBack is the one place the advisory is written, so every reason
// above ends in the same two sentences.
func fallBack(reason string) PublicDir {
	return PublicDir{
		Path: DefaultPublicDir,
		Results: check.Results{Findings: []check.Finding{{
			CheckID:  check.IDPathCharset,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf("%s, so file names were checked under %s/ instead.",
				reason, DefaultPublicDir),
			Why: "Astro copies the public folder into your site unchanged, so the names in " +
				"it are the ones that have to be servable. If your public folder is somewhere " +
				"else, a name there that can't be served fails the deploy after the build " +
				"rather than here.",
			Next: "If your public folder is public/, there is nothing to do. Otherwise, set " +
				"publicDir to a plain path, such as './static', to have it checked before upload.",
		}}},
	}
}
