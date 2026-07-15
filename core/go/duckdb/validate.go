package duckdb

import (
	"fmt"
	"strings"
)

// ErrNotReadOnly is returned when a submitted statement is not a read-only
// SELECT/WITH query. The /sql surface and the MCP query_sql tool are exposed to
// untrusted callers, so writes, DDL, and side-effecting statements are refused.
type ErrNotReadOnly struct{ Reason string }

func (e ErrNotReadOnly) Error() string { return "duckdb: statement rejected: " + e.Reason }

// deniedKeywords are statement-leading or side-effecting keywords refused up
// front for a clear error. This is defence-in-depth: the Engine also wraps the
// query as a subquery (SELECT * FROM (<sql>)), which makes any of these
// syntactically impossible to execute even if one appeared as, say, a CTE name.
var deniedKeywords = []string{
	"insert", "update", "delete", "merge", "create", "drop", "alter", "truncate",
	"attach", "detach", "copy", "install", "load", "pragma", "export", "import",
	"call", "set", "reset", "vacuum", "checkpoint", "grant", "revoke",
}

// validateReadOnly checks that sql is a single read-only query. It returns the
// cleaned (comment-stripped, semicolon-trimmed) statement ready for wrapping.
func validateReadOnly(sql string) (string, error) {
	clean := stripComments(sql)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return "", ErrNotReadOnly{Reason: "empty statement"}
	}
	// Reject embedded statement separators: only a single trailing ';' is allowed.
	trimmed := strings.TrimRight(clean, "; \t\r\n")
	if strings.Contains(trimmed, ";") {
		return "", ErrNotReadOnly{Reason: "multiple statements are not allowed"}
	}
	clean = trimmed

	lower := strings.ToLower(clean)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return "", ErrNotReadOnly{Reason: "only SELECT / WITH queries are allowed"}
	}
	// Whole-word deny-list scan (defence in depth; the subquery wrap is the real
	// guarantee).
	for _, kw := range deniedKeywords {
		if containsWord(lower, kw) {
			return "", ErrNotReadOnly{Reason: fmt.Sprintf("keyword %q is not allowed on the read-only SQL surface", strings.ToUpper(kw))}
		}
	}
	return clean, nil
}

// stripComments removes -- line comments and /* */ block comments so keyword
// scanning and semicolon detection cannot be fooled by commented-out text.
func stripComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		// Line comment.
		if runes[i] == '-' && i+1 < len(runes) && runes[i+1] == '-' {
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		// Block comment.
		if runes[i] == '/' && i+1 < len(runes) && runes[i+1] == '*' {
			i += 2
			for i+1 < len(runes) && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			i++ // skip the closing '/'
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// containsWord reports whether word appears in s delimited by non-identifier
// characters (so "set" does not match "offset" or "settle").
func containsWord(s, word string) bool {
	from := 0
	for {
		idx := strings.Index(s[from:], word)
		if idx < 0 {
			return false
		}
		i := from + idx
		leftOK := i == 0 || !isIdentRune(rune(s[i-1]))
		end := i + len(word)
		rightOK := end >= len(s) || !isIdentRune(rune(s[end]))
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
