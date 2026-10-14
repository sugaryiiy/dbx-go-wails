package server

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf16"
)

type savedSQLSearchMatch struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ConnectionID string `json:"connection_id"`
	Database     string `json:"database"`
	Catalog      string `json:"catalog,omitempty"`
	Schema       string `json:"schema,omitempty"`
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	LineText     string `json:"line_text"`
	MatchText    string `json:"match_text"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
}

type savedSQLSearchResult struct {
	Matches   []savedSQLSearchMatch `json:"matches"`
	Truncated bool                  `json:"truncated"`
}

func (s *Server) searchSavedSQL(ctx context.Context, p map[string]any) (savedSQLSearchResult, error) {
	result := savedSQLSearchResult{Matches: []savedSQLSearchMatch{}}
	connectionID := str(p, "connection_id")
	if strings.TrimSpace(connectionID) == "" {
		return result, errors.New("connection_id is required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	excludedIDs := map[string]bool{}
	if raw, exists := p["exclude_ids"]; exists {
		ids, ok := raw.([]any)
		if !ok {
			return result, errors.New("exclude_ids must be an array of strings")
		}
		for _, rawID := range ids {
			id, ok := rawID.(string)
			if !ok {
				return result, errors.New("exclude_ids must be an array of strings")
			}
			excludedIDs[id] = true
		}
	}
	query := str(p, "query")
	if query == "" {
		return result, nil
	}
	limit := integer(p["limit"], 500)
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	// Match against the original text so Unicode case folding never changes
	// the byte offsets used to calculate CodeMirror's UTF-16 positions.
	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
	if err != nil {
		return result, errors.New("Search query is too large or invalid")
	}
	appendLine := func(file map[string]any, lineText string, lineNumber int) bool {
		indices := pattern.FindAllStringIndex(lineText, limit+1-len(result.Matches))
		byteOffset, utf16Offset := 0, 0
		for _, index := range indices {
			if len(result.Matches) == limit {
				result.Truncated = true
				return false
			}
			start := utf16Offset + savedSQLUTF16Length(lineText[byteOffset:index[0]])
			end := start + savedSQLUTF16Length(lineText[index[0]:index[1]])
			byteOffset, utf16Offset = index[1], end
			result.Matches = append(result.Matches, savedSQLSearchMatch{
				ID: str(file, "id"), Name: str(file, "name"),
				ConnectionID: connectionID, Database: str(file, "database"),
				Catalog: str(file, "catalog"), Schema: str(file, "schema"),
				Line: lineNumber, Column: start + 1, LineText: lineText,
				MatchText: lineText[index[0]:index[1]], Start: start, End: end,
			})
		}
		return true
	}
	for _, file := range s.snapshot().SQLFiles {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if str(file, "connectionId") != connectionID {
			continue
		}
		if excludedIDs[str(file, "id")] {
			continue
		}
		before := len(result.Matches)
		for lineIndex, line := range strings.Split(str(file, "sql"), "\n") {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if !appendLine(file, strings.TrimSuffix(line, "\r"), lineIndex+1) {
				return result, nil
			}
		}
		// Name matches keep saved queries discoverable when their SQL text
		// does not contain the search term. Line zero marks a name match.
		if len(result.Matches) == before && !appendLine(file, str(file, "name"), 0) {
			return result, nil
		}
	}
	return result, nil
}

func savedSQLUTF16Length(text string) int {
	length := 0
	for _, r := range text {
		length += utf16.RuneLen(r)
	}
	return length
}
