package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// writeConfig writes body as the project's astro.config.mjs, or no
// config at all when body is empty, and returns the project root.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body == "" {
		return dir
	}
	if err := os.WriteFile(filepath.Join(dir, "astro.config.mjs"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestResolveFoldersReadsTheFolderTheConfigNames covers every way a
// project says where its public folder is that the read can settle:
// silence, a plain path in its several spellings, and Astro's own
// file-URL idiom. None of them says anything, because none of them is in
// doubt.
func TestResolveFoldersReadsTheFolderTheConfigNames(t *testing.T) {
	for _, tc := range []struct {
		name, config, want string
	}{
		{"no config at all", "", "public"},
		{"a config that never mentions it", "export default {\n  site: 'https://example.com',\n};\n", "public"},
		{"a relative path", "export default {\n  publicDir: './static',\n};\n", "static"},
		{"a bare name", "export default { publicDir: 'static' };\n", "static"},
		{"a nested path with a trailing slash", "export default { publicDir: './assets/static/' };\n", "assets/static"},
		{"the file-URL idiom", "import { defineConfig } from 'astro/config';\n" +
			"import { fileURLToPath } from 'node:url';\n" +
			"export default defineConfig({\n  publicDir: fileURLToPath(new URL('./static/', import.meta.url)),\n});\n", "static"},
		// A config the scan gives up on for a reason that has nothing to
		// do with publicDir. Astro uses the default, so this does too,
		// and in silence: see ResolveFolders.
		{"a template literal elsewhere, publicDir unmentioned",
			"export default { site: `https://${host}` };\n", "public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveFolders(OSFileSystem{}, writeConfig(t, tc.config))
			if got.Public != tc.want {
				t.Errorf("Path = %q, want %q", got.Public, tc.want)
			}
			if len(got.Results.Findings) != 0 {
				t.Errorf("findings = %+v, want none — nothing here is in doubt", got.Results.Findings)
			}
		})
	}
}

// TestResolveFoldersFindsThePagesInsideTheSourceFolder is the pages
// half: `pages` inside a srcDir the read settles, and the default, in
// silence, when it settles none.
func TestResolveFoldersFindsThePagesInsideTheSourceFolder(t *testing.T) {
	for _, tc := range []struct {
		name, config, want string
	}{
		{"no config at all", "", "src/pages"},
		{"a srcDir", "export default { srcDir: './source' };\n", "source/pages"},
		{"a srcDir at the root", "export default { srcDir: '.' };\n", "pages"},
		{"a computed srcDir", "const d = './x';\nexport default { srcDir: d };\n", "src/pages"},
		{"a srcDir outside the project", "export default { srcDir: '../x' };\n", "src/pages"},
		{"a scan that gave up", "export default { srcDir: `./${d}` };\n", "src/pages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveFolders(OSFileSystem{}, writeConfig(t, tc.config))
			if got.Pages != tc.want {
				t.Errorf("Pages = %q, want %q", got.Pages, tc.want)
			}
			if len(got.Results.Findings) != 0 {
				t.Errorf("findings = %+v, want none — the pages-dir check speaks for srcDir", got.Results.Findings)
			}
		})
	}
}

// TestResolveFoldersFallsBackAndSaysSo is the ruled half: a publicDir
// the read cannot settle checks public/ instead, says so in a warning
// naming the fallback, and never refuses.
//
// REQUIRED MUTATION, run 2026-10-03: make fallBack return the default
// with no finding — a SILENT fallback. Every case below reds.
func TestResolveFoldersFallsBackAndSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name, config, reason string
	}{
		{"a computed value", "const dir = './static';\nexport default { publicDir: dir };\n",
			"worked out when the config runs"},
		{"set twice", "export default { publicDir: 'a', publicDir: 'b' };\n",
			"more than once"},
		{"outside the project", "export default { publicDir: '../shared' };\n",
			"escapes the project root"},
		{"an absolute path", "export default { publicDir: '/srv/static' };\n",
			"absolute path"},
		{"a scan that gave up, with publicDir in the file",
			"export default { publicDir: `./${name}` };\n", "names publicDir, but it also uses"},
		// Astro accepts a URL here as well; the reader knows only the
		// idiom wrapped in fileURLToPath, so a bare one falls back.
		{"a bare URL", "export default { publicDir: new URL('./static/', import.meta.url) };\n",
			"worked out when the config runs"},
		{"a shorthand property", "const publicDir = 'static';\nexport default { publicDir };\n",
			"in a way this check doesn't read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveFolders(OSFileSystem{}, writeConfig(t, tc.config))
			if got.Public != DefaultPublicDir {
				t.Errorf("Path = %q, want the default %q", got.Public, DefaultPublicDir)
			}
			if len(got.Results.Findings) != 1 {
				t.Fatalf("findings = %+v, want exactly one warning naming the fallback", got.Results.Findings)
			}
			f := got.Results.Findings[0]
			if f.Severity != check.SeverityWarning {
				t.Errorf("Severity = %q, want a warning — a fallback is never a refusal", f.Severity)
			}
			if f.CheckID != check.IDPathCharset {
				t.Errorf("CheckID = %q, want the file-name check's own id", f.CheckID)
			}
			if !strings.Contains(f.Message, "astro.config.mjs") || !strings.Contains(f.Message, tc.reason) {
				t.Errorf("Message = %q, want it to name the config and say %q", f.Message, tc.reason)
			}
			if !strings.Contains(f.Message, "checked under public/ instead") {
				t.Errorf("Message = %q, want it to name the fallback", f.Message)
			}
			if len(got.Results.Manifest) != 0 {
				t.Errorf("Manifest = %v, want none — the file-name check owns its id", got.Results.Manifest)
			}
		})
	}
}
