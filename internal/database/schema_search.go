package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const objectSourceSearchScanLimit = 5000

// Positions use UTF-16 code units, matching JavaScript strings and the source
// editor. Line and column are one-based; start and end are zero-based positions
// within line_text, end exclusive. Multiline match_text remains complete, while
// the preview highlight ends at the end of its first line.
type ObjectSourceSearchMatch struct {
	ObjectType string `json:"object_type"`
	ObjectName string `json:"object_name"`
	Database   string `json:"database"`
	Schema     string `json:"schema"`
	Signature  string `json:"signature"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	LineText   string `json:"line_text"`
	MatchText  string `json:"match_text"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type ObjectSourceSearchResult struct {
	Matches   []ObjectSourceSearchMatch `json:"matches"`
	Truncated bool                      `json:"truncated"`
	Warnings  []string                  `json:"warnings"`
}

type objectSourceSearchOptions struct {
	pattern        *regexp.Regexp
	wholeWord      bool
	isRegex        bool
	firstMatchOnly bool
	limit          int
}

func parseObjectSourceSearch(p map[string]any) (objectSourceSearchOptions, error) {
	query := str(p, "query")
	if len(query) > 4096 {
		return objectSourceSearchOptions{}, errors.New("Object source search query must not exceed 4096 bytes")
	}
	limit := integer(p, 500, "limit")
	if limit < 1 || limit > 2000 {
		return objectSourceSearchOptions{}, errors.New("Object source search limit must be between 1 and 2000")
	}
	options := objectSourceSearchOptions{wholeWord: boolean(p, "whole_word"), isRegex: boolean(p, "is_regex"), firstMatchOnly: boolean(p, "first_match_only"), limit: limit}
	if query == "" {
		return options, nil
	}
	if !options.isRegex {
		query = regexp.QuoteMeta(query)
	}
	if !boolean(p, "case_sensitive") {
		query = "(?i)" + query
	}
	var err error
	options.pattern, err = regexp.Compile(query)
	if err != nil {
		return objectSourceSearchOptions{}, fmt.Errorf("Invalid object source search expression: %w", err)
	}
	return options, nil
}

func (m *Manager) searchObjectSources(ctx context.Context, p map[string]any) (any, error) {
	options, err := parseObjectSourceSearch(p)
	if err != nil {
		return nil, err
	}
	id := str(p, "connection_id", "connectionId")
	m.mu.RLock()
	c := m.connections[id]
	if c == nil {
		m.mu.RUnlock()
		return nil, errors.New("database connection is not open")
	}
	config := c.config
	m.mu.RUnlock()
	if config.Type != "mysql" && config.Type != "postgres" {
		return nil, unsupported("object source search for " + config.Type)
	}
	result := ObjectSourceSearchResult{Matches: []ObjectSourceSearchMatch{}, Warnings: []string{}}
	if options.pattern == nil {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	database, schema := str(p, "database"), str(p, "schema")
	effectiveDatabase := database
	if effectiveDatabase == "" {
		effectiveDatabase = config.Database
	}
	scope, err := objectSourceSearchVisibility(p, config, effectiveDatabase)
	if err != nil {
		return nil, err
	}
	db, config, _, err := m.pool(ctx, id, database)
	if err != nil {
		return nil, err
	}
	var objects []record
	if config.Type == "mysql" {
		// An omitted database deliberately searches the whole connection, even
		// when the pool itself has a default database selected.
		if database == "" {
			database = schema
		}
		objects, err = mysqlSearchableObjects(ctx, db, database, scope)
	} else {
		if database == "" {
			database = config.Database
		}
		objects, err = postgresSearchableObjectSources(ctx, db, database, schema, scope)
	}
	if err != nil {
		return nil, redact(err, config)
	}
	if len(objects) > objectSourceSearchScanLimit {
		objects = objects[:objectSourceSearchScanLimit]
		result.Truncated = true
		result.Warnings = append(result.Warnings, fmt.Sprintf("Search is limited to the first %d object definitions", objectSourceSearchScanLimit))
	}
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := valueString(object["source"])
		if config.Type == "mysql" {
			source, err = mysqlCatalogObjectSource(ctx, db, valueString(object["database"]), valueString(object["object_name"]), valueString(object["object_type"]))
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s %s.%s: %s", valueString(object["object_type"]), valueString(object["schema"]), valueString(object["object_name"]), redact(err, config)))
				continue
			}
		}
		if source == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s %s.%s: source is unavailable", valueString(object["object_type"]), valueString(object["schema"]), valueString(object["object_name"])))
			continue
		}
		// The editable-source builder trims the raw catalog definition before
		// opening it, so search positions must use the same text.
		matches := searchObjectSource(strings.TrimSpace(source), object, options, options.limit-len(result.Matches)+1)
		result.Matches = append(result.Matches, matches...)
		if len(result.Matches) > options.limit {
			result.Matches = result.Matches[:options.limit]
			result.Truncated = true
			break
		}
	}
	return result, nil
}

type objectSourceNameFilter struct {
	enabled  bool
	exact    []string
	patterns []string
}

type objectSourceSearchScope struct {
	databases objectSourceNameFilter
	schemas   objectSourceNameFilter
}

func objectSourceSearchStringList(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return values, true
	case []any:
		list := make([]string, 0, len(values))
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				return nil, false
			}
			list = append(list, name)
		}
		return list, true
	}
	return nil, false
}

func objectSourceSearchVisibility(p map[string]any, config Config, database string) (objectSourceSearchScope, error) {
	scope := objectSourceSearchScope{}
	if names, exists := p["databases"]; exists {
		var valid bool
		scope.databases.exact, valid = objectSourceSearchStringList(names)
		if !valid {
			return scope, errors.New("Object source search databases must be a string array")
		}
		scope.databases.enabled = true
	} else {
		scope.databases.exact, scope.databases.enabled = objectSourceSearchStringList(config.Raw["visible_databases"])
		if patterns, valid := objectSourceSearchStringList(config.Raw["visible_database_patterns"]); valid {
			for _, pattern := range patterns {
				if pattern = strings.TrimSpace(pattern); pattern != "" {
					scope.databases.patterns = append(scope.databases.patterns, pattern)
					scope.databases.enabled = true
				}
			}
		}
	}
	if names, exists := p["schemas"]; exists {
		// Null explicitly clears a stale filter in the connection snapshot;
		// an empty array remains an explicit selection of no schemas.
		if names != nil {
			var valid bool
			scope.schemas.exact, valid = objectSourceSearchStringList(names)
			if !valid {
				return scope, errors.New("Object source search schemas must be a string array or null")
			}
			scope.schemas.enabled = true
		}
	} else if config.Type != "mysql" || str(p, "database") != "" || str(p, "schema") != "" {
		// MySQL schemas are databases. A per-database schema selection from
		// the default database cannot constrain a whole-connection search.
		var names any
		switch schemas := config.Raw["visible_schemas"].(type) {
		case map[string]any:
			names = schemas[database]
		case map[string][]string:
			if configured, exists := schemas[database]; exists {
				names = configured
			}
		}
		scope.schemas.exact, scope.schemas.enabled = objectSourceSearchStringList(names)
	}
	return scope, nil
}

// Filters run in the metadata query, before the scan limit and before any
// definition is fetched. Exact selections and SQL LIKE patterns form a union,
// matching the sidebar. Request arrays describe its current visible scope and
// override the connection snapshot, so changing visibility needs no reconnect.
func objectSourceFilterSQL(column string, filter objectSourceNameFilter, args *[]any, postgres bool) string {
	if !filter.enabled {
		return ""
	}
	placeholder := func(value string) string {
		*args = append(*args, value)
		if postgres {
			return fmt.Sprintf("$%d", len(*args))
		}
		return "?"
	}
	if !postgres {
		column = "BINARY " + column
	}
	conditions := []string{}
	if len(filter.exact) > 0 {
		parameters := make([]string, len(filter.exact))
		for index, name := range filter.exact {
			parameters[index] = placeholder(name)
		}
		conditions = append(conditions, column+" IN ("+strings.Join(parameters, ",")+")")
	}
	for _, pattern := range filter.patterns {
		conditions = append(conditions, column+" LIKE "+placeholder(strings.ReplaceAll(pattern, "^", "^^"))+" ESCAPE '^'")
	}
	if len(conditions) == 0 {
		return " AND 1=0"
	}
	return " AND (" + strings.Join(conditions, " OR ") + ")"
}

func mysqlSearchableObjects(ctx context.Context, db *sql.DB, database string, scope objectSourceSearchScope) ([]record, error) {
	routines := "SELECT routine_name AS object_name,routine_type AS object_type,routine_schema AS `database`,routine_schema AS `schema` FROM information_schema.routines WHERE routine_schema NOT IN ('information_schema','mysql','performance_schema','sys')"
	events := "SELECT event_name AS object_name,'EVENT' AS object_type,event_schema AS `database`,event_schema AS `schema` FROM information_schema.events WHERE event_schema NOT IN ('information_schema','mysql','performance_schema','sys')"
	args := []any{}
	if database != "" {
		routines += " AND routine_schema=?"
		args = append(args, database)
	}
	routines += objectSourceFilterSQL("routine_schema", scope.databases, &args, false)
	routines += objectSourceFilterSQL("routine_schema", scope.schemas, &args, false)
	if database != "" {
		events += " AND event_schema=?"
		args = append(args, database)
	}
	events += objectSourceFilterSQL("event_schema", scope.databases, &args, false)
	events += objectSourceFilterSQL("event_schema", scope.schemas, &args, false)
	return records(ctx, db, routines+" UNION ALL "+events+" ORDER BY `database`,object_type,object_name LIMIT 5001", args...)
}

func postgresSearchableObjectSources(ctx context.Context, db *sql.DB, database, schema string, scope objectSourceSearchScope) ([]record, error) {
	tx, err := postgresCatalogSnapshot(ctx, db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	kind, condition, err := postgresRoutineCatalogSQL(ctx, tx)
	if err != nil {
		return nil, err
	}
	statement := `SELECT p.proname AS object_name,` + kind + ` AS object_type,$1::text AS database,n.nspname AS schema,pg_get_function_identity_arguments(p.oid) AS signature,pg_get_functiondef(p.oid) AS source FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND ` + condition
	args := []any{database}
	if schema != "" {
		statement += " AND n.nspname=$2"
		args = append(args, schema)
	}
	statement += objectSourceFilterSQL("$1::text", scope.databases, &args, true)
	statement += objectSourceFilterSQL("n.nspname", scope.schemas, &args, true)
	objects, err := records(ctx, tx, statement+" ORDER BY n.nspname,p.proname,signature LIMIT 5001", args...)
	if err != nil {
		return nil, err
	}
	return objects, tx.Commit()
}

func searchObjectSource(source string, object record, options objectSourceSearchOptions, limit int) []ObjectSourceSearchMatch {
	if options.firstMatchOnly && limit > 1 {
		limit = 1
	}
	matches := []ObjectSourceSearchMatch{}
	lineStarts := []int{0}
	for index, character := range source {
		if character == '\n' {
			lineStarts = append(lineStarts, index+1)
		}
	}
	// Literal searches are bounded by the result limit. Word filtering and
	// regex zero-width matches require inspecting all candidates to avoid
	// incorrectly declaring a source exhausted before a later real match.
	candidateLimit := limit
	if options.wholeWord || options.isRegex {
		candidateLimit = -1
	}
	for _, position := range options.pattern.FindAllStringIndex(source, candidateLimit) {
		start, end := position[0], position[1]
		if start == end || (options.wholeWord && !objectSourceWordBoundary(source, start, end)) {
			continue
		}
		lineIndex := sort.Search(len(lineStarts), func(index int) bool { return lineStarts[index] > start }) - 1
		lineStart := lineStarts[lineIndex]
		lineEnd := len(source)
		if lineIndex+1 < len(lineStarts) {
			lineEnd = lineStarts[lineIndex+1] - 1
		}
		lineText := strings.TrimSuffix(source[lineStart:lineEnd], "\r")
		column := objectSourceUTF16Length(source[lineStart:start])
		previewEnd := min(column+objectSourceUTF16Length(source[start:end]), objectSourceUTF16Length(lineText))
		matches = append(matches, ObjectSourceSearchMatch{
			ObjectType: valueString(object["object_type"]), ObjectName: valueString(object["object_name"]),
			Database: valueString(object["database"]), Schema: valueString(object["schema"]), Signature: valueString(object["signature"]),
			Line: lineIndex + 1, Column: column + 1,
			LineText: lineText, MatchText: source[start:end],
			Start: column, End: previewEnd,
		})
		if len(matches) >= limit {
			break
		}
	}
	return matches
}

func objectSourceUTF16Length(text string) int {
	length := 0
	for _, character := range text {
		length++
		if character > 0xffff {
			length++
		}
	}
	return length
}

func objectSourceWordBoundary(source string, start, end int) bool {
	isWord := func(character rune) bool {
		return character == '_' || character == '$' || unicode.IsLetter(character) || unicode.IsDigit(character)
	}
	if start > 0 {
		character, _ := utf8.DecodeLastRuneInString(source[:start])
		if isWord(character) {
			return false
		}
	}
	if end < len(source) {
		character, _ := utf8.DecodeRuneInString(source[end:])
		if isWord(character) {
			return false
		}
	}
	return true
}
