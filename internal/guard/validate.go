package guard

import (
	"fmt"
	"strings"
)

// forbiddenKeywords may never appear as bare words in a statement, even
// inside a SELECT: they mark data-modifying CTEs and SELECT ... INTO.
var forbiddenKeywords = map[string]bool{
	"insert": true,
	"update": true,
	"delete": true,
	"merge":  true,
	"into":   true,
}

// lockingAfterFor are words that, following FOR, make a locking clause
// (FOR UPDATE / FOR NO KEY UPDATE / FOR SHARE / FOR KEY SHARE).
var lockingAfterFor = map[string]bool{"update": true, "no": true, "share": true, "key": true}

// forbiddenFunctions are rejected when called. They either run SQL from a
// string (hiding relations from the plan check), change session settings,
// touch the server filesystem, or affect other sessions.
var forbiddenFunctions = map[string]bool{
	"set_config":                    true,
	"query_to_xml":                  true,
	"query_to_xml_and_xmlschema":    true,
	"query_to_xmlschema":            true,
	"cursor_to_xml":                 true,
	"cursor_to_xmlschema":           true,
	"table_to_xml":                  true,
	"table_to_xml_and_xmlschema":    true,
	"table_to_xmlschema":            true,
	"schema_to_xml":                 true,
	"schema_to_xml_and_xmlschema":   true,
	"schema_to_xmlschema":           true,
	"database_to_xml":               true,
	"database_to_xml_and_xmlschema": true,
	"database_to_xmlschema":         true,
	"ts_stat":                       true,
	"pg_terminate_backend":          true,
	"pg_cancel_backend":             true,
	"pg_reload_conf":                true,
	"pg_rotate_logfile":             true,
	"pg_notify":                     true,
	"pg_stat_file":                  true,
	"pg_sleep":                      true,
	"pg_sleep_for":                  true,
	"pg_sleep_until":                true,
}

// forbiddenFunctionPrefixes cover function families.
var forbiddenFunctionPrefixes = []string{"pg_read_", "pg_ls_", "lo_", "dblink", "pg_advisory_", "pg_try_advisory_"}

func functionForbidden(name string) bool {
	if forbiddenFunctions[name] {
		return true
	}
	for _, p := range forbiddenFunctionPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ValidateSelect checks that sql is exactly one read-only SELECT statement
// (SELECT, WITH ... SELECT, VALUES or TABLE) and returns it with any trailing
// semicolon and comments removed, ready to be wrapped in a subquery.
//
// This is a lexical check, not a full parser. It is deliberately strict and
// is backed by the read-only transaction, the extended query protocol (which
// refuses multiple statements) and the plan-based relation check.
func ValidateSelect(sql string) (string, error) {
	toks, end, err := lex(sql)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDenied, err)
	}
	if len(toks) == 0 {
		return "", fmt.Errorf("%w: empty statement", ErrDenied)
	}

	first := 0
	for first < len(toks) && toks[first].kind == tokPunct && toks[first].text == "(" {
		first++
	}
	if first == len(toks) || toks[first].kind != tokWord {
		return "", fmt.Errorf("%w: only SELECT statements are allowed", ErrDenied)
	}
	switch toks[first].text {
	case "select", "with", "values", "table":
	default:
		return "", fmt.Errorf("%w: only SELECT statements are allowed, got %s", ErrDenied, strings.ToUpper(toks[first].text))
	}

	for i, t := range toks {
		switch t.kind {
		case tokWord:
			if forbiddenKeywords[t.text] {
				return "", fmt.Errorf("%w: %s is not allowed", ErrDenied, strings.ToUpper(t.text))
			}
			if t.text == "for" && i+1 < len(toks) && toks[i+1].kind == tokWord && lockingAfterFor[toks[i+1].text] {
				return "", fmt.Errorf("%w: locking clauses (FOR UPDATE/SHARE) are not allowed", ErrDenied)
			}
		case tokPunct:
			if t.text == ";" {
				return "", fmt.Errorf("%w: multiple statements are not allowed", ErrDenied)
			}
		}
		if (t.kind == tokWord || t.kind == tokQuotedIdent) && i+1 < len(toks) &&
			toks[i+1].kind == tokPunct && toks[i+1].text == "(" && functionForbidden(t.text) {
			return "", fmt.Errorf("%w: function %s is not allowed", ErrDenied, t.text)
		}
	}
	return strings.TrimSpace(sql[:end]), nil
}

type tokKind int

const (
	tokWord        tokKind = iota // unquoted identifier or keyword, lowercased
	tokQuotedIdent                // "identifier", unescaped
	tokString                     // any string literal
	tokNumber
	tokParam // $1
	tokPunct // operators and punctuation, one token per character
)

type token struct {
	kind tokKind
	text string
}

// lex tokenizes sql following PostgreSQL's lexical rules closely enough to
// find statement boundaries, keywords and function calls without being
// fooled by comments, quoted identifiers or string literals. It returns the
// byte offset where the statement ends: the position of a final ";" when it
// is followed only by whitespace and comments, otherwise len(sql). A ";"
// followed by anything else is returned as a token.
func lex(sql string) (toks []token, end int, err error) {
	end = len(sql)
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case isSpace(c) || isCommentStart(sql, i):
			j, err := skipTrivia(sql, i)
			if err != nil {
				return nil, 0, err
			}
			i = j

		case c == '\'':
			j, err := scanQuoted(sql, i, '\'', false)
			if err != nil {
				return nil, 0, err
			}
			toks = append(toks, token{kind: tokString})
			i = j

		case c == '"':
			j, err := scanQuoted(sql, i, '"', false)
			if err != nil {
				return nil, 0, err
			}
			toks = append(toks, token{kind: tokQuotedIdent, text: strings.ReplaceAll(sql[i+1:j-1], `""`, `"`)})
			i = j

		case c == '$':
			if i+1 < n && isDigit(sql[i+1]) {
				j := i + 1
				for j < n && isDigit(sql[j]) {
					j++
				}
				toks = append(toks, token{kind: tokParam, text: sql[i:j]})
				i = j
				break
			}
			j, ok := dollarTag(sql, i)
			if !ok {
				return nil, 0, fmt.Errorf("unexpected $ at offset %d", i)
			}
			tag := sql[i:j]
			k := strings.Index(sql[j:], tag)
			if k < 0 {
				return nil, 0, fmt.Errorf("unterminated dollar-quoted string")
			}
			toks = append(toks, token{kind: tokString})
			i = j + k + len(tag)

		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentCont(sql[j]) {
				j++
			}
			word := strings.ToLower(sql[i:j])
			if j < n && sql[j] == '\'' {
				switch word {
				case "e":
					k, err := scanQuoted(sql, j, '\'', true)
					if err != nil {
						return nil, 0, err
					}
					toks = append(toks, token{kind: tokString})
					i = k
					continue
				case "b", "x", "n":
					k, err := scanQuoted(sql, j, '\'', false)
					if err != nil {
						return nil, 0, err
					}
					toks = append(toks, token{kind: tokString})
					i = k
					continue
				}
			}
			if word == "u" && j < n && sql[j] == '&' {
				return nil, 0, fmt.Errorf("unicode-escaped identifiers and strings (U&) are not supported")
			}
			toks = append(toks, token{kind: tokWord, text: word})
			i = j

		case isDigit(c) || (c == '.' && i+1 < n && isDigit(sql[i+1])):
			j := i + 1
			for j < n && (isIdentCont(sql[j]) || sql[j] == '.') {
				j++
			}
			toks = append(toks, token{kind: tokNumber, text: sql[i:j]})
			i = j

		case c == ';':
			if j, err := skipTrivia(sql, i+1); err == nil && j == n {
				return toks, i, nil
			}
			toks = append(toks, token{kind: tokPunct, text: ";"})
			i++

		case c >= 0x80:
			// Non-ASCII letters are valid identifier characters.
			j := i
			for j < n && (sql[j] >= 0x80 || isIdentCont(sql[j])) {
				j++
			}
			toks = append(toks, token{kind: tokWord, text: strings.ToLower(sql[i:j])})
			i = j

		default:
			toks = append(toks, token{kind: tokPunct, text: string(c)})
			i++
		}
	}
	return toks, end, nil
}

// scanQuoted returns the offset just past the literal starting at sql[start]
// (the opening quote). A doubled quote is an escaped quote; backslash escapes
// apply only to E” strings.
func scanQuoted(sql string, start int, q byte, backslash bool) (int, error) {
	i := start + 1
	for i < len(sql) {
		switch {
		case backslash && sql[i] == '\\':
			i += 2
		case sql[i] == q:
			if i+1 < len(sql) && sql[i+1] == q {
				i += 2
				continue
			}
			return i + 1, nil
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unterminated quoted literal")
}

// dollarTag reports whether a dollar-quote tag ($$ or $tag$) starts at
// sql[i], returning the offset just past it.
func dollarTag(sql string, i int) (int, bool) {
	j := i + 1
	if j < len(sql) && isIdentStart(sql[j]) {
		for j < len(sql) && isIdentCont(sql[j]) && sql[j] != '$' {
			j++
		}
	}
	if j < len(sql) && sql[j] == '$' {
		return j + 1, true
	}
	return 0, false
}

// skipTrivia returns the offset of the first byte at or after i that is not
// whitespace or part of a comment. Block comments nest in PostgreSQL.
func skipTrivia(sql string, i int) (int, error) {
	n := len(sql)
	for i < n {
		switch {
		case isSpace(sql[i]):
			i++
		case sql[i] == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case sql[i] == '/' && i+1 < n && sql[i+1] == '*':
			depth := 0
			for {
				if i+1 >= n {
					return 0, fmt.Errorf("unterminated comment")
				}
				if sql[i] == '/' && sql[i+1] == '*' {
					depth++
					i += 2
				} else if sql[i] == '*' && sql[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		default:
			return i, nil
		}
	}
	return i, nil
}

func isCommentStart(sql string, i int) bool {
	return i+1 < len(sql) && ((sql[i] == '-' && sql[i+1] == '-') || (sql[i] == '/' && sql[i+1] == '*'))
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentCont(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '$' }
