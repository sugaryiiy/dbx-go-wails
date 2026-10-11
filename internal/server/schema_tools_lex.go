package server

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type schemaToken struct {
	start, end int
	identifier string
	symbol     byte
}

// schemaLex exposes only identifier and punctuation tokens; text literals,
// dollar bodies and comments stay opaque, so namespace rewriting cannot edit
// a default, comment or function body accidentally.
func schemaLex(sql string) []schemaToken {
	tokens := []schemaToken{}
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					i++
					if i < len(sql) && sql[i] == '\'' {
						i++
						continue
					}
					break
				}
				if sql[i] == '\\' && i+1 < len(sql) {
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			i += 2
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			i += 2
			depth := 1
			for i+1 < len(sql) && depth > 0 {
				if sql[i] == '/' && sql[i+1] == '*' {
					depth++
					i += 2
				} else if sql[i] == '*' && sql[i+1] == '/' {
					depth--
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(sql) && (sql[j] == '_' || sql[j] >= 'a' && sql[j] <= 'z' || sql[j] >= 'A' && sql[j] <= 'Z' || sql[j] >= '0' && sql[j] <= '9') {
				j++
			}
			if j < len(sql) && sql[j] == '$' {
				tag := sql[i : j+1]
				if end := strings.Index(sql[j+1:], tag); end >= 0 {
					i = j + 1 + end + len(tag)
					continue
				}
			}
		}
		if c == '"' || c == '`' || c == '[' {
			start := i
			close := c
			if c == '[' {
				close = ']'
			}
			i++
			name := strings.Builder{}
			for i < len(sql) {
				if sql[i] == close {
					i++
					if i < len(sql) && sql[i] == close {
						name.WriteByte(close)
						i++
						continue
					}
					break
				}
				name.WriteByte(sql[i])
				i++
			}
			tokens = append(tokens, schemaToken{start: start, end: i, identifier: name.String()})
			continue
		}
		r, size := utf8.DecodeRuneInString(sql[i:])
		if unicode.IsLetter(r) || c == '_' {
			start := i
			i += size
			for i < len(sql) {
				r, size = utf8.DecodeRuneInString(sql[i:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
					break
				}
				i += size
			}
			tokens = append(tokens, schemaToken{start: start, end: i, identifier: sql[start:i]})
			continue
		}
		if c == '.' {
			tokens = append(tokens, schemaToken{start: i, end: i + 1, symbol: c})
		}
		i++
	}
	return tokens
}
func schemaRewriteNamespace(sql, oldSchema, newSchema, kind string) string {
	if oldSchema == "" || newSchema == "" || oldSchema == newSchema {
		return sql
	}
	tokens := schemaLex(sql)
	var out strings.Builder
	offset := 0
	for i, t := range tokens {
		if t.identifier != "" && strings.EqualFold(t.identifier, oldSchema) && i+1 < len(tokens) && tokens[i+1].symbol == '.' {
			out.WriteString(sql[offset:t.start])
			out.WriteString(quoteSQLName(newSchema, kind))
			offset = t.end
		}
	}
	out.WriteString(sql[offset:])
	return out.String()
}
func schemaCreateNamespace(sql string) string {
	tokens := schemaLex(sql)
	for i, t := range tokens {
		if strings.EqualFold(t.identifier, "TABLE") || strings.EqualFold(t.identifier, "VIEW") {
			j := i + 1
			if j+2 < len(tokens) && strings.EqualFold(tokens[j].identifier, "IF") && strings.EqualFold(tokens[j+1].identifier, "NOT") && strings.EqualFold(tokens[j+2].identifier, "EXISTS") {
				j += 3
			}
			if j+2 < len(tokens) && tokens[j+1].symbol == '.' {
				return tokens[j].identifier
			}
			break
		}
	}
	return ""
}
