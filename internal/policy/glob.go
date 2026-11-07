package policy

import (
	"regexp"
	"strings"
	"sync"
)

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

// compileGlob turns a glob into an anchored regexp. In path mode "*" and "?"
// do not cross "/" and "**" matches anything; otherwise "*" matches anything.
func compileGlob(pat string, pathMode bool) *regexp.Regexp {
	key := pat
	if pathMode {
		key = "p:" + pat
	}
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[key]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			if pathMode {
				b.WriteString("[^/]*")
			} else {
				b.WriteString(".*")
			}
		case c == '?':
			if pathMode {
				b.WriteString("[^/]")
			} else {
				b.WriteString(".")
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache[key] = re
	return re
}

// matchArg matches one argv element against a pattern element:
//   - "=literal" matches exactly "literal"
//   - "a|b|c"    matches any alternative; each alternative is a glob where
//     "*" matches any characters (including "/") and "?" one character.
func matchArg(pat, arg string) bool {
	if strings.HasPrefix(pat, "=") {
		return arg == pat[1:]
	}
	for _, alt := range strings.Split(pat, "|") {
		if compileGlob(alt, false).MatchString(arg) {
			return true
		}
	}
	return false
}

// MatchPath matches a cleaned absolute path against a path glob
// ("*" = one segment, "**" = any depth).
func MatchPath(pat, path string) bool {
	return compileGlob(pat, true).MatchString(path)
}

func matchAnyPath(pats []string, paths ...string) (string, bool) {
	for _, p := range pats {
		for _, path := range paths {
			if path != "" && MatchPath(p, path) {
				return p, true
			}
		}
	}
	return "", false
}
