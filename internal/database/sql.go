package database

import (
	"errors"
	"regexp"
	"strings"
)

type sqlStatement struct {
	text  string
	words []string
}

// SplitStatements uses standard SQL quotes and PostgreSQL dollar quotes. The
// engine uses a dialect-specific variant, so SQLite brackets and MySQL comments
// cannot accidentally swallow a separator in another dialect.
func SplitStatements(source string) ([]string, error) { return splitStatements(source, "") }
func SplitStatementsForDialect(source, kind string) ([]string, error) {
	if kind == "mariadb" {
		kind = "mysql"
	}
	return splitStatements(source, kind)
}

var dollarQuoteStart = regexp.MustCompile(`^\$(?:[_\pL][_\pL\pN]*)?\$`)

func splitStatements(source, dialect string) ([]string, error) {
	parsed, e := scanSQL(source, dialect)
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(parsed))
	for _, p := range parsed {
		out = append(out, p.text)
	}
	return out, nil
}
func sqlIdentifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
func sqlLetterByte(b byte) bool { return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }
func sqlSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}
func scanSQL(source, dialect string) ([]sqlStatement, error) {
	result := []sqlStatement{}
	start := 0
	quote := byte(0)
	escapeString := false
	depth := 0
	blockDepth := 0
	lineComment := false
	dollar := ""
	hasCode := false
	words := []string{}
	triggerBody, triggerCases := 0, 0
	sqliteTrigger := false
	appendStatement := func(end int) error {
		if !hasCode {
			return nil
		}
		if len(words) > 0 && words[0] == "DELIMITER" {
			return unsupported("MySQL DELIMITER scripts")
		}
		if len(words) > 1 && words[0] == "CREATE" {
			for _, word := range words[1:] {
				switch word {
				case "TRIGGER", "PROCEDURE", "FUNCTION", "EVENT":
					if dialect == "postgres" || dialect == "sqlite" && word == "TRIGGER" {
						goto normalCreate
					}
					return unsupported("CREATE " + word + " scripts")
				case "TABLE", "VIEW", "INDEX", "DATABASE", "SCHEMA", "SEQUENCE", "TYPE", "EXTENSION", "ROLE", "USER":
					goto normalCreate
				}
			}
		}
	normalCreate:

		result = append(result, sqlStatement{text: strings.TrimSpace(source[start:end]), words: words})
		words = []string{}
		sqliteTrigger = false
		hasCode = false
		return nil
	}
	for i := 0; i < len(source); i++ {
		ch := source[i]
		if lineComment {
			if ch == '\n' || ch == '\r' {
				lineComment = false
			}
			continue
		}
		if blockDepth > 0 {
			if i+1 < len(source) && source[i:i+2] == "/*" {
				if dialect == "mysql" || dialect == "sqlite" {
					return nil, unsupported("nested block comments in this SQL dialect")
				}
				blockDepth++
				i++
			} else if i+1 < len(source) && source[i:i+2] == "*/" {
				blockDepth--
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(source[i:], dollar) {
				i += len(dollar) - 1
				dollar = ""
			}
			continue
		}
		if quote != 0 {
			if ch == '\\' && quote != '[' {
				if dialect == "mysql" {
					return nil, unsupported("backslash-escaped SQL literals; use doubled quotes or parameterized import")
				}
				if escapeString {
					if i+1 < len(source) {
						i++
					}
					continue
				}
			}
			end := quote
			if quote == '[' {
				end = ']'
			}
			if ch == end {
				if i+1 < len(source) && source[i+1] == end {
					i++
				} else {
					quote = 0
					escapeString = false
				}
			}
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "--" && (dialect != "mysql" || i+2 == len(source) || source[i+2] <= 32) {
			lineComment = true
			i++
			continue
		}
		if ch == '#' && dialect == "mysql" {
			lineComment = true
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			if dialect == "mysql" && (strings.HasPrefix(source[i:], "/*!") || strings.HasPrefix(strings.ToUpper(source[i:]), "/*M!")) {
				return nil, unsupported("MySQL executable version comments")
			}
			blockDepth = 1
			i++
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' || ch == '[' && (dialect == "" || dialect == "sqlite") {
			quote = ch
			hasCode = true
			escapeString = ch == '\'' && i > 0 && (source[i-1] == 'E' || source[i-1] == 'e') && (i == 1 || !sqlIdentifierByte(source[i-2])) && (dialect == "postgres" || dialect == "")
			continue
		}
		if ch == '$' && (dialect == "" || dialect == "postgres") && (i == 0 || (!sqlIdentifierByte(source[i-1]) && source[i-1] < 0x80)) {
			if match := dollarQuoteStart.FindString(source[i:]); match != "" {
				dollar = match
				hasCode = true
				i += len(match) - 1
				continue
			}
		}
		if ch == ';' {
			if triggerBody > 0 {
				continue
			}
			if depth != 0 {
				return nil, errors.New("unbalanced SQL parentheses")
			}
			if e := appendStatement(i); e != nil {
				return nil, e
			}
			start = i + 1
			continue
		}
		if ch == '(' {
			depth++
			hasCode = true
			continue
		}
		if ch == ')' {
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced SQL parentheses")
			}
			hasCode = true
			continue
		}
		if sqlLetterByte(ch) {
			j := i + 1
			for j < len(source) && sqlIdentifierByte(source[j]) {
				j++
			}
			if depth == 0 {
				word := strings.ToUpper(source[i:j])
				words = append(words, word)
				if dialect == "sqlite" && len(words) > 1 && words[0] == "CREATE" && word == "TRIGGER" {
					sqliteTrigger = true
				}
				if sqliteTrigger {
					switch word {
					case "BEGIN":
						triggerBody++
					case "CASE":
						if triggerBody > 0 {
							triggerCases++
						}
					case "END":
						if triggerCases > 0 {
							triggerCases--
						} else if triggerBody > 0 {
							triggerBody--
						}
					}
				}
			}
			hasCode = true
			i = j - 1
			continue
		}
		if !sqlSpace(ch) {
			hasCode = true
		}
	}
	if quote != 0 || blockDepth != 0 || dollar != "" {
		return nil, errors.New("unterminated SQL string, identifier, or comment")
	}
	if depth != 0 {
		return nil, errors.New("unbalanced SQL parentheses")
	}
	if triggerBody != 0 || triggerCases != 0 {
		return nil, errors.New("unterminated SQLite trigger body")
	}
	if e := appendStatement(len(source)); e != nil {
		return nil, e
	}
	return result, nil
}
func statementWords(s string, dialect ...string) []string {
	kind := ""
	if len(dialect) > 0 {
		kind = dialect[0]
	}
	parsed, e := scanSQL(s, kind)
	if e != nil || len(parsed) != 1 {
		return nil
	}
	return parsed[0].words
}
func firstWord(s string, dialect ...string) string {
	words := statementWords(s, dialect...)
	if len(words) > 0 {
		return words[0]
	}
	return ""
}
func mainVerb(s string, dialect ...string) string {
	words := statementWords(s, dialect...)
	if len(words) == 0 {
		return ""
	}
	if words[0] != "WITH" {
		return words[0]
	}
	for _, w := range words[1:] {
		switch w {
		case "SELECT", "VALUES", "INSERT", "UPDATE", "DELETE", "MERGE":
			return w
		}
	}
	return ""
}
func ordinaryRead(s string, dialect ...string) bool {
	switch mainVerb(s, dialect...) {
	case "SELECT", "VALUES", "SHOW":
		return true
	}
	return false
}
func returnsRows(s string, dialect ...string) bool {
	switch mainVerb(s, dialect...) {
	case "SELECT", "VALUES", "SHOW", "EXPLAIN", "PRAGMA", "DESCRIBE", "DESC":
		return true
	case "CALL":
		// MySQL procedures may return a SELECT result. QueryContext also handles
		// procedures that only return an OK packet as an empty result set.
		if len(dialect) > 0 && (dialect[0] == "mysql" || dialect[0] == "mariadb") {
			return true
		}
	}
	return containsSQLWord(s, "RETURNING", dialect...)
}
func containsSQLWord(s, word string, dialect ...string) bool {
	for _, w := range statementWords(s, dialect...) {
		if w == word {
			return true
		}
	}
	return false
}
