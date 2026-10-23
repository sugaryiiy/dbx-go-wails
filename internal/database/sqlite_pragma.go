package database

import (
	"regexp"
	"strings"
)

const sqlitePragmaName = "(?:[A-Za-z_][A-Za-z0-9_]*|\"(?:\"\"|[^\"])*\"|'(?:''|[^'])*'|`(?:``|[^`])*`|\\[[^\\]]*\\])"

var sqlitePragma = regexp.MustCompile("(?is)^\\s*PRAGMA\\s+(?:" + sqlitePragmaName + "\\s*\\.\\s*)?(" + sqlitePragmaName + ")(.*)$")

func sqliteConnectionPragma(statement string) bool {
	if firstWord(statement, "sqlite") != "PRAGMA" {
		return false
	}
	match := sqlitePragma.FindStringSubmatch(statement)
	if len(match) != 3 {
		// Unusual quoting or intervening comments must never allow a settings
		// change to escape back into the shared pool.
		return true
	}
	name := strings.ToLower(match[1])
	if strings.ContainsAny(name[:1], "\"'`[") {
		name = name[1 : len(name)-1]
	}
	switch name {
	case "table_info", "table_xinfo", "index_info", "index_xinfo", "index_list", "foreign_key_list", "database_list", "compile_options", "pragma_list", "function_list", "module_list", "collation_list":
		return false
	}
	// A bare PRAGMA reads its value; assignments and argument syntax change
	// it. Treat commented/unknown trailing syntax conservatively as stateful.
	return strings.TrimSpace(match[2]) != ""
}
