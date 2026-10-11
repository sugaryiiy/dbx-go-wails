package database

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The descriptor is not an authorization token. Locked connections rebuild the
// complete query and compare it byte for byte, so a forged descriptor cannot
// authorize arbitrary Flux or an appended write operation.
const influxPreviewPrefix = "// DBX InfluxDB preview: "

type InfluxPreviewOptions struct {
	Version      string            `json:"version"`
	Database     string            `json:"database"`
	Table        string            `json:"table"`
	Columns      []string          `json:"columns,omitempty"`
	Where        string            `json:"where,omitempty"`
	Order        string            `json:"order,omitempty"`
	Limit        int               `json:"limit"`
	Offset       int               `json:"offset,omitempty"`
	DefaultTime  bool              `json:"defaultTime,omitempty"`
	Count        bool              `json:"count,omitempty"`
	Empty        bool              `json:"empty,omitempty"`
	TagRenames   map[string]string `json:"tagRenames,omitempty"`
	FieldRenames map[string]string `json:"fieldRenames,omitempty"`
}

func BuildInfluxPreview(o InfluxPreviewOptions) (string, error) {
	if o.Version != "1" && o.Version != "2" {
		return "", errors.New("InfluxDB preview requires version 1 or 2")
	}
	if o.Table == "" || o.Version == "2" && o.Database == "" {
		return "", errors.New("InfluxDB database and measurement are required")
	}
	for _, name := range append([]string{o.Database, o.Table}, o.Columns...) {
		if strings.ContainsRune(name, 0) {
			return "", errors.New("Invalid InfluxDB identifier")
		}
	}
	for _, renames := range []map[string]string{o.TagRenames, o.FieldRenames} {
		for name, alias := range renames {
			if name == "" || alias == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(alias, 0) {
				return "", errors.New("Invalid InfluxDB column alias")
			}
		}
	}
	if o.Limit < 0 || o.Limit > 100000 || o.Offset < 0 {
		return "", errors.New("Invalid InfluxDB pagination")
	}
	where := strings.TrimSpace(o.Where)
	if strings.HasPrefix(strings.ToUpper(where), "WHERE ") {
		where = strings.TrimSpace(where[6:])
	}
	predicate, err := influxFilter(where)
	if err != nil {
		return "", err
	}
	order, desc, err := influxOrder(o.Order)
	if err != nil {
		return "", err
	}
	if len(order) == 0 {
		order, desc = []string{"time"}, true
	}
	if o.Version == "1" {
		if o.Count {
			// COUNT(*) counts each field separately in InfluxQL, not wide rows.
			return "", errors.New("Exact table row counts are unavailable for InfluxDB 1.x")
		}
		selection := "*"
		if len(o.Columns) > 0 {
			names := []string{}
			for _, name := range o.Columns {
				if name != "time" {
					names = append(names, influxIdentifier(name))
				}
			}
			if len(names) > 0 {
				selection = strings.Join(names, ", ")
			}
		}
		if where == "" && o.DefaultTime {
			where = "time > now() - 5m"
		}
		if len(order) != 1 || order[0] != "time" {
			return "", errors.New("InfluxDB 1.x supports table ordering by time only")
		}
		source := "SELECT " + selection + " FROM " + influxIdentifier(o.Table)
		if where != "" {
			source += " WHERE " + where
		}
		direction := "ASC"
		if desc {
			direction = "DESC"
		}
		source += " ORDER BY time " + direction
		if o.Limit > 0 {
			source += fmt.Sprintf(" LIMIT %d OFFSET %d", o.Limit, o.Offset)
		}
		return source, nil
	}
	start := "0"
	if where == "" && o.DefaultTime {
		start = "-5m"
	}
	lines := []string{}
	if strings.Contains(predicate, "date.sub(") {
		lines = append(lines, `import "date"`)
	}
	lines = append(lines, "from(bucket: "+fluxString(o.Database)+")",
		"  |> range(start: "+start+")",
		"  |> filter(fn: (r) => r._measurement == "+fluxString(o.Table)+")")
	for _, name := range influxSortedKeys(o.TagRenames) {
		// A rename function works when individual series omit this tag column.
		lines = append(lines, "  |> rename(fn: (column) => if column == "+fluxString(name)+" then "+fluxString(o.TagRenames[name])+" else column)")
	}
	for _, name := range influxSortedKeys(o.FieldRenames) {
		lines = append(lines, "  |> map(fn: (r) => ({r with _field: if r._field == "+fluxString(name)+" then "+fluxString(o.FieldRenames[name])+" else r._field}))")
	}
	lines = append(lines,
		`  |> pivot(rowKey: ["_time"], columnKey: ["_field"], valueColumn: "_value")`,
		`  |> drop(columns: ["_start", "_stop", "_measurement"])`,
		`  |> rename(columns: {_time: "time"})`)
	if predicate != "" {
		lines = append(lines, "  |> filter(fn: (r) => "+predicate+")")
	}
	if o.Count {
		lines = append(lines, `  |> map(fn: (r) => ({_value: 1}))`, `  |> group(columns: [])`,
			`  |> count(column: "_value")`, `  |> rename(columns: {_value: "cnt"})`, `  |> keep(columns: ["cnt"])`)
	} else {
		lines = append(lines, `  |> group(columns: [])`, "  |> sort(columns: "+influxFluxList(order)+", desc: "+strconv.FormatBool(desc)+")")
		if o.Empty {
			lines = append(lines, "  |> limit(n: 0)")
		} else if o.Limit > 0 {
			lines = append(lines, fmt.Sprintf("  |> limit(n: %d, offset: %d)", o.Limit, o.Offset))
		}
		if len(o.Columns) > 0 {
			lines = append(lines, "  |> keep(columns: "+influxFluxList(o.Columns)+")")
		}
	}
	descriptor, _ := json.Marshal(o)
	return strings.Join(lines, "\n") + "\n" + influxPreviewPrefix + base64.RawURLEncoding.EncodeToString(descriptor), nil
}

func influxSortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ParseInfluxPreview(source string) (InfluxPreviewOptions, bool) {
	var o InfluxPreviewOptions
	if len(source) > 1024*1024 {
		return o, false
	}
	index := strings.LastIndex(source, "\n"+influxPreviewPrefix)
	if index < 0 {
		return o, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(source[index+1+len(influxPreviewPrefix):])
	if err != nil || json.Unmarshal(raw, &o) != nil || o.Version != "2" {
		return o, false
	}
	rebuilt, err := BuildInfluxPreview(o)
	return o, err == nil && rebuilt == source
}

func influxFluxList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fluxString(name)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

type influxToken struct{ kind, value string }

var influxNumericToken = regexp.MustCompile(`^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?`)
var influxDurationToken = regexp.MustCompile(`^\d+(?:ns|us|ms|s|m|h|d|w)$`)

// A deliberately small WHERE grammar, shared with the read-only validator.
// Unknown expressions fail explicitly instead of being ignored or interpolated.
func influxTokens(source string) ([]influxToken, error) {
	tokens := []influxToken{}
	for len(source) > 0 {
		if unicode.IsSpace(rune(source[0])) {
			source = source[1:]
			continue
		}
		c := source[0]
		if c == '\'' || c == '"' || c == '`' {
			kind := "identifier"
			if c == '\'' {
				kind = "string"
			}
			i := 1
			var value strings.Builder
			closed := false
			for i < len(source) {
				if source[i] == c {
					if i+1 < len(source) && source[i+1] == c {
						value.WriteByte(c)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				if source[i] == '\\' {
					// SQL escaping varies by dialect; require doubled quotes.
					return nil, errors.New("Use doubled quotes in InfluxDB preview filters")
				}
				value.WriteByte(source[i])
				i++
			}
			if !closed {
				return nil, errors.New("Unclosed InfluxDB filter identifier or string")
			}
			tokens = append(tokens, influxToken{kind, value.String()})
			source = source[i:]
			continue
		}
		if strings.ContainsRune("(),+-", rune(c)) {
			tokens = append(tokens, influxToken{string(c), string(c)})
			source = source[1:]
			continue
		}
		if strings.ContainsRune("=<>!", rune(c)) {
			i := 1
			if len(source) > 1 && strings.ContainsRune("=>", rune(source[1])) {
				i++
			}
			op := source[:i]
			if op != "=" && op != "!=" && op != "<>" && op != "<" && op != ">" && op != "<=" && op != ">=" {
				return nil, errors.New("Unsupported InfluxDB comparison operator")
			}
			tokens = append(tokens, influxToken{"operator", op})
			source = source[i:]
			continue
		}
		if n := influxNumericToken.FindString(source); n != "" {
			i := len(n)
			for i < len(source) && source[i] >= 'a' && source[i] <= 'z' {
				i++
			}
			kind := "number"
			if i > len(n) {
				kind = "duration"
				if !influxDurationToken.MatchString(source[:i]) {
					return nil, errors.New("Unsupported InfluxDB duration")
				}
			}
			tokens = append(tokens, influxToken{kind, source[:i]})
			source = source[i:]
			continue
		}
		i := 0
		for i < len(source) && (source[i] == '_' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || source[i] >= '0' && source[i] <= '9' || source[i] >= 128) {
			i++
		}
		if i == 0 {
			return nil, errors.New("Unsupported InfluxDB preview expression; use comparisons joined by AND/OR")
		}
		tokens = append(tokens, influxToken{"word", source[:i]})
		source = source[i:]
	}
	return tokens, nil
}

type influxPredicateParser struct {
	tokens []influxToken
	pos    int
}

func (p *influxPredicateParser) take(value string) bool {
	if p.pos < len(p.tokens) && strings.EqualFold(p.tokens[p.pos].value, value) && p.tokens[p.pos].kind != "identifier" && p.tokens[p.pos].kind != "string" {
		p.pos++
		return true
	}
	return false
}

func (p *influxPredicateParser) expression(level int) (string, error) {
	if level > 64 {
		return "", errors.New("InfluxDB filter is too deeply nested")
	}
	left, err := p.andExpression(level + 1)
	for err == nil && p.take("OR") {
		var right string
		right, err = p.andExpression(level + 1)
		left = "(" + left + " or " + right + ")"
	}
	return left, err
}
func (p *influxPredicateParser) andExpression(level int) (string, error) {
	left, err := p.comparison(level + 1)
	for err == nil && p.take("AND") {
		var right string
		right, err = p.comparison(level + 1)
		left = "(" + left + " and " + right + ")"
	}
	return left, err
}
func (p *influxPredicateParser) comparison(level int) (string, error) {
	if p.take("(") {
		value, err := p.expression(level + 1)
		if err != nil || !p.take(")") {
			return "", errors.New("Invalid InfluxDB filter parentheses")
		}
		return "(" + value + ")", nil
	}
	if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != "word" && p.tokens[p.pos].kind != "identifier" {
		return "", errors.New("InfluxDB filter requires a column comparison")
	}
	name := p.tokens[p.pos].value
	p.pos++
	column := "r[" + fluxString(name) + "]"
	if p.take("IS") {
		not := p.take("NOT")
		if !p.take("NULL") {
			return "", errors.New("InfluxDB IS filter requires NULL")
		}
		if not {
			return "exists " + column, nil
		}
		return "not exists " + column, nil
	}
	if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != "operator" {
		return "", errors.New("Unsupported InfluxDB preview filter operator")
	}
	op := p.tokens[p.pos].value
	p.pos++
	if op == "=" {
		op = "=="
	}
	if op == "<>" {
		op = "!="
	}
	numeric := p.pos < len(p.tokens) && (p.tokens[p.pos].kind == "number" || p.tokens[p.pos].kind == "-" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].kind == "number")
	value, err := p.literal(name == "time")
	if err != nil {
		return "", err
	}
	comparisonColumn := column
	if numeric && name != "time" {
		// Flux numeric comparisons require matching types. Preview metadata does
		// not distinguish integer and floating point field keys.
		comparisonColumn, value = "float(v: "+column+")", "float(v: "+value+")"
	}
	return "(exists " + column + " and " + comparisonColumn + " " + op + " " + value + ")", nil
}
func (p *influxPredicateParser) literal(timestamp bool) (string, error) {
	negative := p.take("-")
	if p.pos >= len(p.tokens) {
		return "", errors.New("InfluxDB filter value is required")
	}
	token := p.tokens[p.pos]
	p.pos++
	if token.kind == "number" {
		if negative {
			return "-" + token.value, nil
		}
		return token.value, nil
	}
	if negative {
		return "", errors.New("Invalid InfluxDB negative value")
	}
	if token.kind == "string" {
		value := fluxString(token.value)
		if timestamp {
			value = "time(v: " + value + ")"
		}
		return value, nil
	}
	if token.kind == "word" && (strings.EqualFold(token.value, "TRUE") || strings.EqualFold(token.value, "FALSE")) {
		return strings.ToLower(token.value), nil
	}
	if timestamp && token.kind == "word" && strings.EqualFold(token.value, "now") && p.take("(") && p.take(")") {
		if p.take("-") {
			if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != "duration" {
				return "", errors.New("Time filter requires a duration after now() -")
			}
			duration := p.tokens[p.pos].value
			p.pos++
			return "date.sub(d: " + duration + ", from: now())", nil
		}
		return "now()", nil
	}
	return "", errors.New("Unsupported InfluxDB filter value")
}

func influxFilter(source string) (string, error) {
	if source == "" {
		return "", nil
	}
	if len(source) > 65536 {
		return "", errors.New("InfluxDB filter is too long")
	}
	tokens, err := influxTokens(source)
	if err != nil {
		return "", err
	}
	p := influxPredicateParser{tokens: tokens}
	value, err := p.expression(0)
	if err == nil && p.pos != len(tokens) {
		err = errors.New("Unsupported trailing InfluxDB filter expression")
	}
	return value, err
}

func influxOrder(source string) ([]string, bool, error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(strings.ToUpper(source), "ORDER BY ") {
		source = strings.TrimSpace(source[9:])
	}
	tokens, err := influxTokens(source)
	if err != nil {
		return nil, false, err
	}
	columns := []string{}
	desc := false
	for i := 0; i < len(tokens); {
		if tokens[i].kind != "identifier" && tokens[i].kind != "word" {
			return nil, false, errors.New("InfluxDB sort requires column names")
		}
		columns = append(columns, tokens[i].value)
		i++
		currentDesc := false
		if i < len(tokens) && tokens[i].kind == "word" && (strings.EqualFold(tokens[i].value, "ASC") || strings.EqualFold(tokens[i].value, "DESC")) {
			currentDesc = strings.EqualFold(tokens[i].value, "DESC")
			i++
		}
		if len(columns) > 1 && currentDesc != desc {
			return nil, false, errors.New("InfluxDB composite sort requires the same direction for every column")
		}
		desc = currentDesc
		if i < len(tokens) {
			if tokens[i].kind != "," || i+1 == len(tokens) {
				return nil, false, errors.New("Invalid InfluxDB order expression")
			}
			i++
		}
	}
	return columns, desc, nil
}
