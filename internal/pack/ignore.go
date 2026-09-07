package pack

import "strings"

// The ignore-rule matcher.
//
// IT IS WRITTEN HERE RATHER THAN TAKEN AS A DEPENDENCY, under this
// repository's stdlib-first policy. The standard library's own pattern
// matcher is close and not close enough: it has no notion of a pattern
// that floats to any depth, no double-star, and its bracket expressions
// negate with a different character — so a project using any of those
// would have its rules silently half-applied, which is the exact failure
// that ends with somebody's excluded file on a public website. A
// third-party matcher would remove that objection and add a dependency
// this binary would otherwise not have, in a package whose correctness
// is checked against the real program in the differential row either
// way. With that comparison in place, the argument for the dependency is
// convenience alone.
//
// WHAT IS DELIBERATELY NOT READ: anything outside the project directory.
// No machine-wide configuration, no per-user ignore file, nothing inside
// the version-control directory. The archive this walk feeds has to be a
// function of the project alone — if it also depended on the developer's
// own settings, two people packing the same commit would produce
// different sites and the difference would be invisible to both.

const gitignoreName = ".gitignore"

// ignoreFile is one parsed ignore file together with the directory it
// governs: patterns in it are relative to that directory, and it has no
// authority outside it.
type ignoreFile struct {
	// dir is slash-separated and relative to the project root, empty at
	// the root itself.
	dir      string
	patterns []ignorePattern
}

// ignorePattern is one line, compiled.
//
// The line number is kept because it is what a person is pointed at when
// they ask why a file went missing, and because it is what the real
// program reports when the two are compared.
type ignorePattern struct {
	negate  bool
	dirOnly bool
	segs    []patSeg
	line    int
}

// patSeg is one slash-separated piece of a pattern. A piece that is
// exactly two asterisks spans whole directories; anything else matches a
// single name.
type patSeg struct {
	anySegments bool
	text        string
}

func parseIgnoreFile(dir string, data []byte) *ignoreFile {
	f := &ignoreFile{dir: dir}
	for i, raw := range strings.Split(string(data), "\n") {
		if p, ok := parsePattern(raw, i+1); ok {
			f.patterns = append(f.patterns, p)
		}
	}
	return f
}

// parsePattern compiles one line, or reports that the line is not a
// pattern at all.
//
// THE ORDER OF THE TESTS IS THE SPECIFICATION. The comment mark is
// recognised on the RAW first byte, so an escaped one is a literal name
// and not an empty line; trailing spaces are dropped only after that,
// and only when unescaped; the negation mark is read after the trim, so
// that a backslash in front of it survives into the pattern and is
// matched as an ordinary character.
func parsePattern(raw string, line int) (ignorePattern, bool) {
	// A file written on Windows arrives with the carriage return still
	// attached, and a rule that kept it would match nothing at all —
	// silently, which is the worst way for an exclusion to fail.
	s := strings.TrimSuffix(raw, "\r")
	if s == "" || s[0] == '#' {
		return ignorePattern{}, false
	}
	if s = trimUnescapedTrailingSpaces(s); s == "" {
		return ignorePattern{}, false
	}

	p := ignorePattern{line: line}
	if s[0] == '!' {
		p.negate = true
		s = s[1:]
	}
	if strings.HasSuffix(s, "/") {
		p.dirOnly = true
		s = strings.TrimSuffix(s, "/")
	}
	if s == "" {
		return ignorePattern{}, false
	}

	// A separator anywhere but at the very end anchors the pattern to
	// the directory holding the ignore file. Without one, the pattern
	// floats and matches at any depth below it — which is expressed here
	// as a leading span rather than as a flag, so the matcher has one
	// rule instead of two.
	anchored := strings.Contains(s, "/")
	s = strings.TrimPrefix(s, "/")

	for _, seg := range strings.Split(s, "/") {
		if seg == "" {
			continue
		}
		p.segs = append(p.segs, patSeg{anySegments: seg == "**", text: seg})
	}
	if len(p.segs) == 0 {
		return ignorePattern{}, false
	}
	if !anchored {
		p.segs = append([]patSeg{{anySegments: true}}, p.segs...)
	}
	return p, true
}

// trimUnescapedTrailingSpaces drops the run of spaces at the end of a
// line, unless a backslash protects it.
//
// A backslash anywhere in the line resets the run, which is what makes
// an escaped space in the MIDDLE of a name safe as well as one at the
// end: the bytes after it are ordinary again.
func trimUnescapedTrailingSpaces(s string) string {
	last := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			if last < 0 {
				last = i
			}
		case '\\':
			i++
			if i >= len(s) {
				return s
			}
			last = -1
		default:
			last = -1
		}
	}
	if last >= 0 {
		return s[:last]
	}
	return s
}

// relative reports rel as this file's own directory sees it, or false
// when the path is outside its authority.
func (f *ignoreFile) relative(rel string) (string, bool) {
	if f.dir == "" {
		return rel, true
	}
	if strings.HasPrefix(rel, f.dir+"/") {
		return rel[len(f.dir)+1:], true
	}
	return "", false
}

// ignoredBy is the verdict of the whole stack for one path, where the
// stack runs outermost-first and rel is relative to the project root.
//
// TWO PRECEDENCE RULES, AND THEY NEST. The deeper file wins over the
// shallower one, so the stack is read from the end; within one file the
// last matching line wins, so its patterns are read from the end too. A
// matcher that got either direction backwards would still produce
// plausible answers on most projects and the wrong answer on the ones
// that bothered to write a nested rule.
func ignoredBy(stack []*ignoreFile, rel string, isDir bool) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		f := stack[i]
		sub, ok := f.relative(rel)
		if !ok {
			continue
		}
		for j := len(f.patterns) - 1; j >= 0; j-- {
			if f.patterns[j].match(sub, isDir) {
				return !f.patterns[j].negate
			}
		}
	}
	return false
}

func (p ignorePattern) match(rel string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	return matchSegs(p.segs, strings.Split(rel, "/"))
}

// matchSegs matches a compiled pattern against a path's components.
func matchSegs(segs []patSeg, parts []string) bool {
	if len(segs) == 0 {
		return len(parts) == 0
	}
	head := segs[0]
	if head.anySegments {
		// A span at the END matches what is INSIDE and not the thing
		// itself: a rule written to exclude a directory's contents does
		// not exclude the directory. Everywhere else the span may match
		// no components at all, which is what makes one pattern cover
		// both a file directly inside a directory and one further down.
		if len(segs) == 1 {
			return len(parts) >= 1
		}
		for i := 0; i <= len(parts); i++ {
			if matchSegs(segs[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if !matchName(head.text, parts[0]) {
		return false
	}
	return matchSegs(segs[1:], parts[1:])
}

// matchName matches one pattern component against one name.
//
// IT WORKS IN BYTES, and that is a decision rather than an oversight:
// the program this matcher is checked against does the same, so a
// single-character wildcard consumes one byte there and has to consume
// one byte here. Matching runes instead would be defensible in
// isolation and would disagree with the real thing on any name outside
// ASCII.
//
// The single backtrack point is enough because a component holds no
// separator, so there is never more than one wildcard run to reconsider
// at a time.
func matchName(pat, name string) bool {
	p, n := 0, 0
	starP, starN := -1, 0
	for n < len(name) {
		if p < len(pat) {
			if pat[p] == '*' {
				starP, starN = p, n
				p++
				continue
			}
			if next, ok := matchByte(pat, p, name[n]); ok {
				p, n = next, n+1
				continue
			}
		}
		if starP >= 0 {
			starN++
			p, n = starP+1, starN
			continue
		}
		return false
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}

// matchByte consumes the pattern element at p against one byte, and
// reports where the pattern continues.
func matchByte(pat string, p int, b byte) (int, bool) {
	switch pat[p] {
	case '?':
		return p + 1, true
	case '[':
		return matchClass(pat, p, b)
	case '\\':
		if p+1 < len(pat) {
			return p + 2, pat[p+1] == b
		}
		return p + 1, b == '\\'
	default:
		return p + 1, pat[p] == b
	}
}

// matchClass consumes a bracket expression and reports whether it
// accepts b.
//
// An unterminated bracket is treated as a literal one, which is what
// keeps a name containing a stray bracket from being matched by
// accident.
func matchClass(pat string, p int, b byte) (int, bool) {
	i := p + 1
	negated := false
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		negated = true
		i++
	}

	matched := false
	first := true
	for i < len(pat) {
		if pat[i] == ']' && !first {
			i++
			if negated {
				return i, !matched
			}
			return i, matched
		}
		first = false

		if named, next, ok := namedClassAt(pat, i); ok {
			if inNamedClass(named, b) {
				matched = true
			}
			i = next
			continue
		}

		lo := pat[i]
		if lo == '\\' && i+1 < len(pat) {
			i++
			lo = pat[i]
		}
		i++

		if i+1 < len(pat) && pat[i] == '-' && pat[i+1] != ']' {
			hi := pat[i+1]
			j := i + 1
			if hi == '\\' && j+1 < len(pat) {
				j++
				hi = pat[j]
			}
			i = j + 1
			if lo <= b && b <= hi {
				matched = true
			}
			continue
		}
		if lo == b {
			matched = true
		}
	}
	return p + 1, b == '['
}

// namedClassAt recognises a named character class, which real ignore
// files do use and which nothing else here would understand.
func namedClassAt(pat string, i int) (name string, next int, ok bool) {
	if i+1 >= len(pat) || pat[i] != '[' || pat[i+1] != ':' {
		return "", 0, false
	}
	end := strings.Index(pat[i+2:], ":]")
	if end < 0 {
		return "", 0, false
	}
	return pat[i+2 : i+2+end], i + 2 + end + 2, true
}

func inNamedClass(name string, b byte) bool {
	switch name {
	case "alpha":
		return isUpper(b) || isLower(b)
	case "digit":
		return b >= '0' && b <= '9'
	case "alnum":
		return isUpper(b) || isLower(b) || (b >= '0' && b <= '9')
	case "upper":
		return isUpper(b)
	case "lower":
		return isLower(b)
	case "space":
		return b == ' ' || b == '\t' || b == '\n' || b == '\v' || b == '\f' || b == '\r'
	case "blank":
		return b == ' ' || b == '\t'
	case "cntrl":
		return b < 0x20 || b == 0x7f
	case "print":
		return b >= 0x20 && b < 0x7f
	case "graph":
		return b > 0x20 && b < 0x7f
	case "punct":
		return b > 0x20 && b < 0x7f && !isUpper(b) && !isLower(b) && !(b >= '0' && b <= '9')
	case "xdigit":
		return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
	}
	return false
}

func isUpper(b byte) bool { return b >= 'A' && b <= 'Z' }
func isLower(b byte) bool { return b >= 'a' && b <= 'z' }
