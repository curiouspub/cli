// Command formula writes the Homebrew formula for a release from that
// release's own checksum file.
//
// THE RELEASE TOOL CAN WRITE A FORMULA, AND IT IS NOT USED FOR IT. Its
// formula block is deprecated, and its own validator refuses a
// configuration that uses one; that validator is a gate in this
// repository's test suite, and a gate kept green by leaving the check
// out is not a gate. So the release workflow runs this instead, over the
// checksum file the same run produced.
//
// WHAT IT WRITES: one formula, named for the package, installing the
// binary `curious` on four platforms (macOS and Linux, on Intel and Arm),
// each from its own archive and each pinned by that archive's sha256 as
// the checksum file states it. An archive the file does not list is an
// error, not a gap in the formula: a formula missing a platform installs
// nothing there, and says nothing about why.
//
// THE DOWNLOAD BASE IS AN INPUT. The release workflow passes the address
// its own release lives at, so no address is written into this program.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// platform is one archive the formula installs from.
type platform struct {
	os, arch string
}

// platforms are the four the formula covers, in the order the formula
// lists them. Windows is not among them: the package manager this formula
// is for does not run there.
var platforms = []platform{
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
}

// exactVersion is a release version with no leading v: the archive names
// carry it that way.
var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// input is everything the formula is made from.
type input struct {
	Version      string
	DownloadBase string
	Homepage     string
	Description  string
	License      string
	Checksums    map[string]string // archive name -> sha256
}

// archiveName is the release's own naming for a platform's archive.
func archiveName(version string, p platform) string {
	return fmt.Sprintf("curious_%s_%s_%s.tar.gz", version, p.os, p.arch)
}

// parseChecksums reads a checksum file in the form the release writes:
// one "<sha256>  <name>" per line.
func parseChecksums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !sha256Hex.MatchString(fields[0]) {
			return nil, fmt.Errorf("checksum file line %d is not \"<sha256>  <name>\": %q", n, line)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("checksum file names %s twice", name)
		}
		out[name] = fields[0]
	}
	return out, sc.Err()
}

// render writes the formula, or refuses with the reason.
func render(in input) (string, error) {
	if !exactVersion.MatchString(in.Version) {
		return "", fmt.Errorf("version %q is not one exact release (and carries no leading v)", in.Version)
	}
	for _, f := range []struct{ name, value string }{
		{"download base", in.DownloadBase}, {"homepage", in.Homepage},
		{"description", in.Description}, {"license", in.License},
	} {
		if strings.TrimSpace(f.value) == "" || strings.ContainsAny(f.value, "\"\\\n") {
			return "", fmt.Errorf("the %s is empty or carries a quote, backslash or newline: %q", f.name, f.value)
		}
	}
	sums := map[platform]string{}
	for _, p := range platforms {
		sum, ok := in.Checksums[archiveName(in.Version, p)]
		if !ok {
			return "", fmt.Errorf("the checksum file does not list %s, so the formula could not install on %s/%s",
				archiveName(in.Version, p), p.os, p.arch)
		}
		sums[p] = sum
	}
	base := strings.TrimRight(in.DownloadBase, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by the release workflow from this release's checksums.txt.\n")
	fmt.Fprintf(&b, "# Edits here are overwritten by the next release.\n")
	fmt.Fprintf(&b, "class Curiouspub < Formula\n")
	fmt.Fprintf(&b, "  desc \"%s\"\n", in.Description)
	fmt.Fprintf(&b, "  homepage \"%s\"\n", in.Homepage)
	fmt.Fprintf(&b, "  version \"%s\"\n", in.Version)
	fmt.Fprintf(&b, "  license \"%s\"\n", in.License)
	for _, osName := range []string{"darwin", "linux"} {
		block := map[string]string{"darwin": "on_macos", "linux": "on_linux"}[osName]
		fmt.Fprintf(&b, "\n  %s do\n", block)
		for _, p := range platforms {
			if p.os != osName {
				continue
			}
			cond := map[string]string{"amd64": "Hardware::CPU.intel?", "arm64": "Hardware::CPU.arm?"}[p.arch]
			fmt.Fprintf(&b, "    if %s\n", cond)
			fmt.Fprintf(&b, "      url \"%s/%s\"\n", base, archiveName(in.Version, p))
			fmt.Fprintf(&b, "      sha256 \"%s\"\n", sums[p])
			fmt.Fprintf(&b, "    end\n")
		}
		fmt.Fprintf(&b, "  end\n")
	}
	fmt.Fprintf(&b, "\n  def install\n")
	fmt.Fprintf(&b, "    bin.install \"curious\"\n")
	fmt.Fprintf(&b, "  end\n")
	fmt.Fprintf(&b, "\n  def caveats\n")
	fmt.Fprintf(&b, "    <<~EOS\n")
	for _, line := range caveatLines {
		if line == "" {
			fmt.Fprintf(&b, "\n")
			continue
		}
		fmt.Fprintf(&b, "      %s\n", line)
	}
	fmt.Fprintf(&b, "    EOS\n")
	fmt.Fprintf(&b, "  end\n")
	fmt.Fprintf(&b, "\n  test do\n")
	fmt.Fprintf(&b, "    assert_match version.to_s, shell_output(\"#{bin}/curious version\")\n")
	fmt.Fprintf(&b, "  end\n")
	fmt.Fprintf(&b, "end\n")
	return b.String(), nil
}

// caveatLines is what the formula tells whoever installs it, for the
// releases in which the older cask still exists.
//
// THE CASK AND THE FORMULA BOTH CLAIM THE curious COMMAND, and removing
// the cask afterwards takes the command away. What happens at install
// depends on the package manager's version: an earlier one skipped
// linking this formula while the cask owned the command, and a current
// one takes the command over from the cask. Either way, once the cask is
// removed the command is gone until the formula is linked. So there are
// two migrations, not one, and which applies depends on whether the
// formula is already installed. Both are spelled out in full: a caveat is
// read once, at the moment the command it names is needed, and a reader
// who has to work out the second from the first has been handed a puzzle
// instead of an answer. The cask's caveat and the README carry the same
// two commands, and a guard holds all three to them.
var caveatLines = []string{
	"Replacing the older cask, curious? Installing this formula takes over",
	"the curious command, and removing the cask afterwards takes the command",
	"away again. Remove the cask, then link the formula:",
	"  brew uninstall --cask curious && brew link curiouspub/tap/curiouspub",
	"",
	"To remove the cask before installing this formula instead:",
	"  brew uninstall --cask curious && brew install curiouspub/tap/curiouspub",
}

func main() {
	version := flag.String("version", "", "the release version, without a leading v")
	checksums := flag.String("checksums", "dist/checksums.txt", "the release's checksum file")
	base := flag.String("download-base", "", "the address the release's archives are downloaded from")
	homepage := flag.String("homepage", "", "the project's homepage")
	desc := flag.String("desc", "", "the one-line description")
	license := flag.String("license", "MIT", "the licence identifier")
	out := flag.String("out", "", "where to write the formula")
	flag.Parse()

	f, err := os.Open(*checksums)
	if err != nil {
		fmt.Fprintln(os.Stderr, "formula:", err)
		os.Exit(1)
	}
	sums, err := parseChecksums(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "formula:", err)
		os.Exit(1)
	}
	text, err := render(input{
		Version: strings.TrimPrefix(*version, "v"), DownloadBase: *base,
		Homepage: *homepage, Description: *desc, License: *license, Checksums: sums,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "formula:", err)
		os.Exit(1)
	}
	if *out == "" {
		fmt.Print(text)
		return
	}
	if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "formula:", err)
		os.Exit(1)
	}
}
