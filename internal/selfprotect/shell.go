package selfprotect

import (
	"errors"
	"strings"
)

// A small POSIX-shell reader, enough to tell which words of a command line are
// files a command WRITES, which it only reads, and which commands run where.
// It understands quoting, escapes, ; && || | & and newlines, ( ) { } groups,
// $( ) and backtick substitutions, <( ) >( ) process substitution, heredocs,
// comments and redirections. It does not expand variables (other than ~ and
// $HOME, which match() normalises) or globs: self-protection is a tripwire.
//
// When it cannot read a command (unbalanced quotes, say), Check falls back to
// the old conservative rule: any write verb plus any protected path blocks.

var errUnbalanced = errors.New("unbalanced quote or bracket")

// simpleCmd is one command: its words (quotes removed) and redirections.
type simpleCmd struct {
	args   []string
	redirs []redir
	// cwd is the directory this command runs in, after any `cd` earlier in
	// the same line ("" = the tool call's cwd).
	cwd string
}

type redir struct {
	op     string // > >> >| &> &>> <> < <<< (heredoc bodies are dropped)
	target string
}

func (r redir) writes() bool {
	switch r.op {
	case ">", ">>", ">|", "&>", "&>>", "<>":
		return true
	}
	return false
}

// parseShell splits a command line into simple commands, recursing into
// substitutions and groups. Nested commands come out flat, in order.
func parseShell(src string) ([]simpleCmd, error) {
	return parseWith(src, false)
}

// parseWith: literalBackslash keeps "\" as an ordinary character, the way
// PowerShell and cmd read C:\Users\dev. Check parses both ways and blocks if
// either reading would write a protected path.
func parseWith(src string, literalBackslash bool) ([]simpleCmd, error) {
	p := &parser{src: src, lit: literalBackslash}
	cmds, err := p.list(0)
	if err != nil {
		return nil, err
	}
	if p.i < len(p.src) {
		return nil, errUnbalanced // a stray closing bracket
	}
	applyCd(cmds)
	return cmds, nil
}

type parser struct {
	lit      bool
	src      string
	i        int
	heredocs []heredoc
	depth    int
}

type heredoc struct {
	delim string
	strip bool // <<- strips leading tabs
}

// list reads commands until EOF or the closing byte `end` (0 = EOF).
func (p *parser) list(end byte) ([]simpleCmd, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 64 {
		return nil, errUnbalanced
	}
	var out []simpleCmd
	cur := simpleCmd{}
	flush := func() {
		if len(cur.args) > 0 || len(cur.redirs) > 0 {
			out = append(out, cur)
		}
		cur = simpleCmd{}
	}
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == '\n':
			p.i++
			flush()
			p.readHeredocBodies()
		case c == ' ' || c == '\t' || c == '\r':
			p.i++
		case c == '\\' && p.peek(1) == '\n':
			p.i += 2
		case c == '#' && p.atWordStart():
			for p.i < len(p.src) && p.src[p.i] != '\n' {
				p.i++
			}
		case end != 0 && c == end:
			p.i++
			flush()
			return out, nil
		case c == ';' || c == '&' || c == '|':
			// ; ;; & && | || |&  (and &> which is a redirection, below)
			if c == '&' && p.peek(1) == '>' {
				op, target, nested, err := p.redirection()
				if err != nil {
					return nil, err
				}
				out = append(out, nested...)
				cur.redirs = append(cur.redirs, redir{op, target})
				continue
			}
			p.i++
			for p.i < len(p.src) && (p.src[p.i] == ';' || p.src[p.i] == '&' || p.src[p.i] == '|') {
				p.i++
			}
			flush()
		case c == '(':
			p.i++
			flush()
			inner, err := p.list(')')
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		case c == ')' || c == '}' && end == '}':
			if end == 0 {
				return nil, errUnbalanced
			}
			// `}` closes a { } group only as its own word.
			p.i++
			flush()
			return out, nil
		case c == '{' && p.atWordStart() && (p.peek(1) == ' ' || p.peek(1) == '\n' || p.peek(1) == '\t'):
			p.i++
			flush()
			inner, err := p.list('}')
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		case (c == '<' || c == '>') && p.peek(1) == '(':
			// process substitution: a nested command; the word itself is a pipe.
			p.i += 2
			inner, err := p.list(')')
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			cur.args = append(cur.args, "/dev/fd/63")
		case c == '<' || c == '>' || (isDigit(c) && p.fdRedirect()):
			op, target, nested, err := p.redirection()
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			if op == "<<" || op == "<<-" {
				p.heredocs = append(p.heredocs, heredoc{delim: target, strip: op == "<<-"})
				continue
			}
			cur.redirs = append(cur.redirs, redir{op, target})
		default:
			w, nested, err := p.word()
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			cur.args = append(cur.args, w)
		}
	}
	if end != 0 {
		return nil, errUnbalanced
	}
	flush()
	return out, nil
}

func (p *parser) peek(n int) byte {
	if p.i+n < len(p.src) {
		return p.src[p.i+n]
	}
	return 0
}

func (p *parser) atWordStart() bool {
	if p.i == 0 {
		return true
	}
	switch p.src[p.i-1] {
	case ' ', '\t', '\n', ';', '&', '|', '(', ')':
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// fdRedirect: digits immediately followed by < or > ("2>", "10>>").
func (p *parser) fdRedirect() bool {
	if !p.atWordStart() {
		return false
	}
	j := p.i
	for j < len(p.src) && isDigit(p.src[j]) {
		j++
	}
	return j < len(p.src) && (p.src[j] == '<' || p.src[j] == '>')
}

// redirection reads an operator and its target word. Duplications (2>&1,
// >&2, <&-) have no file target and come back with target "".
func (p *parser) redirection() (op, target string, nested []simpleCmd, err error) {
	for p.i < len(p.src) && isDigit(p.src[p.i]) {
		p.i++
	}
	rest := p.src[p.i:]
	for _, o := range []string{"&>>", "&>", "<<<", "<<-", "<<", "<>", ">>", ">|", ">&", "<&", ">", "<"} {
		if strings.HasPrefix(rest, o) {
			op = o
			break
		}
	}
	if op == "" {
		return "", "", nil, errUnbalanced
	}
	p.i += len(op)
	p.skipBlanks()
	if op == ">&" || op == "<&" {
		j := p.i
		for j < len(p.src) && (isDigit(p.src[j]) || p.src[j] == '-') {
			j++
		}
		if j > p.i && (j == len(p.src) || isTerminator(p.src[j])) {
			p.i = j
			return op, "", nil, nil
		}
		if op == ">&" {
			op = "&>" // bash: ">& file" is "&> file"
		}
	}
	if p.i >= len(p.src) || isTerminator(p.src[p.i]) {
		return op, "", nil, nil
	}
	target, nested, err = p.word()
	return op, target, nested, err
}

func (p *parser) skipBlanks() {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t') {
		p.i++
	}
}

func isTerminator(c byte) bool { return strings.IndexByte(" \t\r\n;&|()<>", c) >= 0 }

// word reads one word, removing quotes. Substitutions inside it are parsed
// and returned as nested commands; in the word they become "$(…)".
func (p *parser) word() (string, []simpleCmd, error) {
	var b strings.Builder
	var nested []simpleCmd
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case isTerminator(c):
			return b.String(), nested, nil
		case c == '\\' && p.lit:
			b.WriteByte(c)
			p.i++
		case c == '\\':
			if p.i+1 < len(p.src) {
				if p.src[p.i+1] != '\n' {
					b.WriteByte(p.src[p.i+1])
				}
				p.i += 2
			} else {
				p.i++
			}
		case c == '\'':
			j := strings.IndexByte(p.src[p.i+1:], '\'')
			if j < 0 {
				return "", nil, errUnbalanced
			}
			b.WriteString(p.src[p.i+1 : p.i+1+j])
			p.i += j + 2
		case c == '"':
			p.i++
			closed := false
			for p.i < len(p.src) {
				d := p.src[p.i]
				if d == '"' {
					p.i++
					closed = true
					break
				}
				if d == '\\' && !p.lit && p.i+1 < len(p.src) && strings.IndexByte("\"\\$`\n", p.src[p.i+1]) >= 0 {
					if p.src[p.i+1] != '\n' {
						b.WriteByte(p.src[p.i+1])
					}
					p.i += 2
					continue
				}
				if d == '$' && p.peek(1) == '(' || d == '`' {
					inner, err := p.substitution()
					if err != nil {
						return "", nil, err
					}
					nested = append(nested, inner...)
					b.WriteString("$(…)")
					continue
				}
				b.WriteByte(d)
				p.i++
			}
			if !closed {
				return "", nil, errUnbalanced
			}
		case c == '$' && p.peek(1) == '\'':
			// $'…' (ANSI-C quoting): keep the text, drop simple escapes.
			p.i += 2
			for p.i < len(p.src) && p.src[p.i] != '\'' {
				if p.src[p.i] == '\\' && p.i+1 < len(p.src) {
					p.i++
				}
				b.WriteByte(p.src[p.i])
				p.i++
			}
			if p.i >= len(p.src) {
				return "", nil, errUnbalanced
			}
			p.i++
		case c == '$' && p.peek(1) == '(' || c == '`':
			inner, err := p.substitution()
			if err != nil {
				return "", nil, err
			}
			nested = append(nested, inner...)
			b.WriteString("$(…)")
		case c == '$' && p.peek(1) == '{':
			j := strings.IndexByte(p.src[p.i:], '}')
			if j < 0 {
				return "", nil, errUnbalanced
			}
			b.WriteString(p.src[p.i : p.i+j+1])
			p.i += j + 1
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return b.String(), nested, nil
}

// substitution parses $( … ), $(( … )) or ` … ` at p.i.
func (p *parser) substitution() ([]simpleCmd, error) {
	if p.src[p.i] == '`' {
		j := p.i + 1
		for j < len(p.src) && p.src[j] != '`' {
			if p.src[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(p.src) {
			return nil, errUnbalanced
		}
		inner := strings.ReplaceAll(p.src[p.i+1:j], "\\`", "`")
		p.i = j + 1
		sub := &parser{src: inner, depth: p.depth, lit: p.lit}
		return sub.list(0)
	}
	if strings.HasPrefix(p.src[p.i:], "$((") {
		// arithmetic: no commands inside (bar nested $( ), ignored)
		depth := 0
		for j := p.i + 1; j < len(p.src); j++ {
			switch p.src[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					p.i = j + 1
					return nil, nil
				}
			}
		}
		return nil, errUnbalanced
	}
	p.i += 2
	return p.list(')')
}

// readHeredocBodies skips the bodies of heredocs opened on the line just
// ended. Their text is data, not commands (a quoted body is never expanded,
// and an unquoted one only expands $( ), which we do not need to see).
func (p *parser) readHeredocBodies() {
	for _, h := range p.heredocs {
		for p.i < len(p.src) {
			nl := strings.IndexByte(p.src[p.i:], '\n')
			line := p.src[p.i:]
			if nl >= 0 {
				line = p.src[p.i : p.i+nl]
			}
			if nl < 0 {
				p.i = len(p.src)
			} else {
				p.i += nl + 1
			}
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
	p.heredocs = nil
}

// applyCd tracks `cd DIR` / `pushd DIR` so later commands on the same line
// are judged in the directory they really run in.
func applyCd(cmds []simpleCmd) {
	cwd := ""
	for i := range cmds {
		cmds[i].cwd = cwd
		a := cmds[i].args
		if len(a) == 0 {
			continue
		}
		if a[0] == "cd" || a[0] == "pushd" || a[0] == "Set-Location" || a[0] == "sl" || a[0] == "chdir" {
			dir := "~"
			for _, x := range a[1:] {
				if !strings.HasPrefix(x, "-") {
					dir = x
					break
				}
			}
			if cwd != "" && !strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "~") && !strings.HasPrefix(dir, "$") && !isWinAbs(dir) {
				dir = cwd + "/" + dir
			}
			cwd = dir
		}
	}
}
