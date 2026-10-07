package database

import (
	"errors"
	"strings"
)

func init() { nativeOpeners["clickhouse"] = openClickHouse }

// The supported ClickHouse subset deliberately excludes caller-selected output
// formats and settings. In protected mode a scalar/table-function allowlist is
// an additional guard; the server's readonly=1 is the actual write boundary.
func clickHouseStatements(source string, protected bool) ([]string, error) {
	if strings.ContainsRune(source, '\\') {
		return nil, unsupported("ClickHouse backslash SQL escapes; use doubled quotes")
	}
	statements, err := splitStatements(source, "clickhouse")
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, v := range strings.Fields("ABS AVG COUNT SUM MIN MAX ROUND LOWER UPPER LENGTH CHAR_LENGTH CHARACTER_LENGTH SUBSTR SUBSTRING TRIM LTRIM RTRIM REPLACE POSITION COALESCE IFNULL NULLIF IF MULTIIF CONCAT CONCAT_WS LEFT RIGHT REVERSE REPEAT LPAD RPAD HEX UNHEX CAST ACCURATECAST ACCURATECASTORNULL TOINT8 TOINT16 TOINT32 TOINT64 TOINT128 TOINT256 TOUINT8 TOUINT16 TOUINT32 TOUINT64 TOUINT128 TOUINT256 TOFLOAT32 TOFLOAT64 TOSTRING TODECIMAL32 TODECIMAL64 TODECIMAL128 TODECIMAL256 TODATE TODATE32 TODATETIME TODATETIME64 TOUNIXTIMESTAMP FORMATDATETIME PARSEDATETIMEBESTEFFORT YEAR MONTH DAY HOUR MINUTE SECOND TOYEAR TOMONTH TODAYOFMONTH TOHOUR TOMINUTE TOSECOND DATE_TRUNC DATE_DIFF DATEDIFF DATEADD ADDDAYS ADDMONTHS NOW NOW64 TODAY YESTERDAY FLOOR CEIL CEILING MODULO POW POWER SQRT EXP LOG LOG2 LOG10 SIGN ROUND SLEEP SLEEPEACHROW CITYHASH64 SIPHASH64 MD5 SHA256 ROW_NUMBER RANK DENSE_RANK LAG LEAD FIRST_VALUE LAST_VALUE NTH_VALUE NTILE PERCENT_RANK CUME_DIST VERSION CURRENTDATABASE CURRENTUSER ISNULL ISNOTNULL ISNAN ISFINITE ASSUMENOTNULL TONULLABLE NULLIF IN NOT EXISTS AS VALUES SELECT DISTINCT OVER PARTITION FILTER ON USING INT8 INT16 INT32 INT64 UINT8 UINT16 UINT32 UINT64 FLOAT32 FLOAT64 DECIMAL DECIMAL32 DECIMAL64 DECIMAL128 DECIMAL256 FIXEDSTRING STRING DATETIME DATETIME64 NULLABLE LOWCARDINALITY ARRAY TUPLE MAP ARRAYJOIN ARRAYMAP ARRAYFILTER ARRAYELEMENT ARRAYLENGTH ARRAYCONCAT TUPLEELEMENT NUMBERS NUMBERS_MT RANGE GROUPARRAY UNIQ UNIQEXACT COUNTIF SUMIF AVGIF MINIF MAXIF ANY ANYLAST ARGMIN ARGMAX QUANTILE QUANTILES QUANTILEEXACT MEDIAN JSONEXTRACT JSONEXTRACTSTRING JSONEXTRACTINT JSONEXTRACTFLOAT JSONHAS") {
		allowed[v] = true
	}
	for _, statement := range statements {
		verb := mainVerb(statement, "clickhouse")
		switch verb {
		case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "INSERT", "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "OPTIMIZE":
		default:
			return nil, unsupported("ClickHouse statement " + verb + "; sessions and transactions are unavailable")
		}
		if protected && verb != "SELECT" && verb != "SHOW" && verb != "DESCRIBE" && verb != "DESC" && verb != "EXPLAIN" {
			return nil, errors.New("writes are locked for this read-only or production connection")
		}
		tokens, err := mcpSQLTokens(statement, "clickhouse")
		if err != nil {
			return nil, err
		}
		for i, token := range tokens {
			upper := strings.ToUpper(token)
			switch upper {
			case "SETTINGS", "FORMAT", "OUTFILE":
				return nil, unsupported("ClickHouse query SETTINGS, FORMAT and external file output")
			}
			if protected {
				switch upper {
				case "INTO", "INSERT", "UPDATE", "DELETE", "ALTER", "DROP", "TRUNCATE", "CREATE", "RENAME", "OPTIMIZE":
					return nil, errors.New("writes are locked for this read-only or production connection")
				}
				if i+1 < len(tokens) && tokens[i+1] == "(" && mcpIdentifier(token) {
					if !allowed[upper] || i > 0 && tokens[i-1] == "." {
						return nil, errors.New("unrecognized or external ClickHouse functions are unavailable in protected queries")
					}
				}
			}
		}
	}
	return statements, nil
}
