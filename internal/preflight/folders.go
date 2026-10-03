package preflight

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/curiouspub/cli/internal/check"
)

// DefaultPublicDir is where Astro looks for the files it copies into the
// built site unchanged, when the config does not say otherwise.
const DefaultPublicDir = "public"

// Folders are where the names that reach the built site live, and what
// the read that found them has to say.
//
// THEY EXIST BECAUSE THE FILE-NAME CHECK HAD BEEN ASKING THE WRONG TREE.
// The platform's rule about which characters a name may carry is a rule
// about the files it stores and serves, which are the build's OUTPUT.
// Two folders put names there. Astro copies the public folder into the
// output byte for byte. And a page's route keeps every character of its
// name outside the brackets, so `café.astro` is served at `café/`.
// Everything else is compiled away. A page called `[slug].astro` is
// Astro's documented way of writing a dynamic route, and checking every
// source file refused every project with a blog.
type Folders struct {
	// Public is the folder Astro copies unchanged, project-relative and
	// slash-separated. "." means the project root itself.
	Public string

	// Pages is the folder whose files become routes: `pages` inside the
	// source folder.
	Pages string

	// Results carries the warning when the configured public folder
	// could not be read and Public is the default instead. It claims no
	// check: the file-name check owns the id, and this is that check
	// saying where it looked.
	Results check.Results
}

// DefaultPagesDir is where Astro looks for pages when the config does not
// move the source folder.
const DefaultPagesDir = "src/pages"

// ResolveFolders reads publicDir and srcDir out of the project's
// astro.config, with public/ and src/pages as the defaults.
//
// A PUBLIC FOLDER IT CANNOT READ FALLS BACK TO THE DEFAULT AND SAYS SO,
// and never refuses. Not knowing where the public folder is costs, at
// worst, a name refused after the build rather than before it; refusing
// the deploy over it would make a working project undeployable because
// of how its config is written.
//
// AND IT SAYS SO ONLY WHEN THE CONFIG NAMES publicDir. A config whose
// scan gave up for another reason — a template literal, a spread — and
// never mentions the key gets the default in silence, because that is
// what Astro uses for it. Warning there would repeat a measured mistake:
// a check that said "I could not confirm this" on most real configs,
// where nothing was wrong. What that leaves silent is a publicDir set
// inside a spread from another file, which a scan of this one file
// cannot see by construction.
//
// THE SOURCE FOLDER FALLS BACK IN SILENCE. The pages-dir check already
// says everything there is to say about a srcDir it cannot read, in the
// same report; saying it twice is noise.
func ResolveFolders(fsys FS, root string) Folders {
	configPath, _, unchecked, found := findConfig(fsys, root)
	if !found {
		if len(unchecked) > 0 {
			return fallBack(fmt.Sprintf("%s couldn't be checked", joinWithAnd(unchecked)), DefaultPagesDir)
		}
		return Folders{Public: DefaultPublicDir, Pages: DefaultPagesDir}
	}
	configName := filepath.Base(configPath)

	content, err := readConfigCapped(fsys, configPath)
	if err != nil {
		return fallBack(fmt.Sprintf("%s couldn't be read (%v)", configName, err), DefaultPagesDir)
	}

	parsed := parseAstroConfig(content)
	pages := pagesDirOf(parsed)
	switch {
	case parsed.unresolved:
		if !parsed.publicDirMentioned {
			return Folders{Public: DefaultPublicDir, Pages: pages}
		}
		return fallBack(fmt.Sprintf("%s names publicDir, but it also uses %s, so its value "+
			"couldn't be read", configName, parsed.unresolvedReason), pages)
	case !parsed.publicDirFound:
		if !parsed.publicDirMentioned {
			return Folders{Public: DefaultPublicDir, Pages: pages}
		}
		return fallBack(fmt.Sprintf("%s names publicDir in a way this check doesn't read",
			configName), pages)
	case parsed.publicDirAmbiguous:
		return fallBack(fmt.Sprintf("%s sets publicDir more than once", configName), pages)
	case !parsed.publicDirResolved:
		return fallBack(fmt.Sprintf("%s sets publicDir to a value worked out when the "+
			"config runs, which this check doesn't run", configName), pages)
	}

	resolved, rejectReason := resolveSrcDirPath(parsed.publicDirValue)
	if rejectReason != "" {
		return fallBack(fmt.Sprintf("%s sets publicDir to %q, which %s", configName,
			parsed.publicDirValue, rejectReason), pages)
	}
	return Folders{Public: resolved, Pages: pages}
}

// pagesDirOf is the pages folder a parsed config implies: `pages` inside
// a srcDir the read settled, or the default.
func pagesDirOf(parsed astroConfig) string {
	if parsed.unresolved || !parsed.srcDirFound || parsed.srcDirAmbiguous || !parsed.srcDirResolved {
		return DefaultPagesDir
	}
	resolved, rejectReason := resolveSrcDirPath(parsed.srcDirValue)
	if rejectReason != "" {
		return DefaultPagesDir
	}
	return path.Join(resolved, "pages")
}

// fallBack is the one place the advisory is written, so every reason
// above ends in the same two sentences.
func fallBack(reason, pages string) Folders {
	return Folders{
		Public: DefaultPublicDir,
		Pages:  pages,
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
