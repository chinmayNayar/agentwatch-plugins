// Package lazyre compiles a regular expression on first use. The hook is a
// short-lived process: compiling every pattern at start-up cost ~2 ms per
// call, most of it for patterns the call never uses.
package lazyre

import (
	"regexp"
	"sync"
)

// RE is a regexp compiled on first use.
type RE struct {
	once sync.Once
	src  string
	re   *regexp.Regexp
}

// New panics on first use, not at import, if src is invalid; tests compile
// every pattern (see lazyre_test.go and each package's tests).
func New(src string) *RE { return &RE{src: src} }

func (r *RE) R() *regexp.Regexp {
	r.once.Do(func() { r.re = regexp.MustCompile(r.src) })
	return r.re
}

func (r *RE) String() string                       { return r.src }
func (r *RE) MatchString(s string) bool            { return r.R().MatchString(s) }
func (r *RE) FindString(s string) string           { return r.R().FindString(s) }
func (r *RE) FindStringSubmatch(s string) []string { return r.R().FindStringSubmatch(s) }
func (r *RE) FindAllStringSubmatch(s string, n int) [][]string {
	return r.R().FindAllStringSubmatch(s, n)
}
func (r *RE) ReplaceAllString(s, repl string) string { return r.R().ReplaceAllString(s, repl) }
func (r *RE) ReplaceAllStringFunc(s string, f func(string) string) string {
	return r.R().ReplaceAllStringFunc(s, f)
}
func (r *RE) Split(s string, n int) []string { return r.R().Split(s, n) }
