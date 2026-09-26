package main

import (
	"fmt"
	"strings"
	"testing"
)

func sumFor(i int) string { return strings.Repeat(fmt.Sprintf("%x", i%16), 64) }

// A checksum file in the release's own shape: the four formula archives,
// plus the ones the formula does not use (Windows, and the bare binaries
// the npm wrapper fetches), which must be ignored rather than refused.
func releaseChecksums(version string) string {
	var b strings.Builder
	i := 1
	for _, p := range platforms {
		fmt.Fprintf(&b, "%s  %s\n", sumFor(i), archiveName(version, p))
		i++
	}
	fmt.Fprintf(&b, "%s  curious_%s_windows_amd64.zip\n", sumFor(9), version)
	fmt.Fprintf(&b, "%s  curious_%s_linux_amd64.gz\n", sumFor(10), version)
	return b.String()
}

func goodInput(t *testing.T) input {
	t.Helper()
	sums, err := parseChecksums(strings.NewReader(releaseChecksums("0.1.1")))
	if err != nil {
		t.Fatal(err)
	}
	return input{Version: "0.1.1", DownloadBase: "https://example.test/releases/download/v0.1.1",
		Homepage: "https://example.test/", Description: "A description", License: "MIT", Checksums: sums}
}

// Four platforms, each with its own archive and that archive's own sha256,
// the binary named curious, and a test that checks the installed version.
func TestTheFormulaPinsEachPlatformToItsOwnArchive(t *testing.T) {
	text, err := render(goodInput(t))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i, p := range platforms {
		url := fmt.Sprintf("url \"https://example.test/releases/download/v0.1.1/%s\"\n      sha256 \"%s\"",
			archiveName("0.1.1", p), sumFor(i+1))
		if !strings.Contains(text, url) {
			t.Errorf("the formula does not pin %s/%s to its archive and sha256:\n%s", p.os, p.arch, text)
		}
	}
	for _, want := range []string{
		"class Curiouspub < Formula", `bin.install "curious"`, `version "0.1.1"`,
		`shell_output("#{bin}/curious version")`, "on_macos do", "on_linux do",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the formula lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "windows") || strings.Contains(text, "_amd64.gz\"") {
		t.Errorf("the formula names an archive it does not install from:\n%s", text)
	}
}

// Every way the input can be wrong is refused with the reason, and no
// formula is written.
func TestAFormulaThatCouldBeWrongIsNotWritten(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*input)
		want   string
	}{
		{"an archive the checksum file does not list", func(in *input) {
			delete(in.Checksums, archiveName("0.1.1", platform{"linux", "arm64"}))
		}, "linux/arm64"},
		{"a version with a leading v", func(in *input) { in.Version = "v0.1.1" }, "exact release"},
		{"a version that is a range", func(in *input) { in.Version = "0.1" }, "exact release"},
		{"no download base", func(in *input) { in.DownloadBase = "" }, "download base"},
		{"a quote in the description", func(in *input) { in.Description = `say "hi"` }, "description"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := goodInput(t)
			tc.mutate(&in)
			text, err := render(in)
			if err == nil {
				t.Fatalf("render wrote a formula:\n%s", text)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("render refused with %q, which does not name %q", err, tc.want)
			}
		})
	}
}

// The checksum file is read strictly: a line that is not a sha256 and a
// name, or a name listed twice, is refused rather than guessed at.
func TestTheChecksumFileIsReadStrictly(t *testing.T) {
	for _, bad := range []string{
		"not-a-sha  curious_0.1.1_linux_amd64.tar.gz\n",
		sumFor(1) + "\n",
		sumFor(1) + "  a.tar.gz\n" + sumFor(2) + "  a.tar.gz\n",
	} {
		if _, err := parseChecksums(strings.NewReader(bad)); err == nil {
			t.Errorf("parseChecksums accepted %q", bad)
		}
	}
	got, err := parseChecksums(strings.NewReader(sumFor(3) + " *curious_0.1.1_darwin_arm64.tar.gz\n"))
	if err != nil || got["curious_0.1.1_darwin_arm64.tar.gz"] != sumFor(3) {
		t.Errorf("a binary-mode line was not read: %v %v", got, err)
	}
}
