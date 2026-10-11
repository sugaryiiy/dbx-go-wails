package server

import (
	"errors"
	"strings"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
)

func influxPreviewOptions(o map[string]any, count bool) database.InfluxPreviewOptions {
	version := strings.TrimPrefix(strings.ToLower(str(o, "influxDbVersion")), "v")
	if version == "" {
		version = strings.TrimPrefix(strings.ToLower(str(o, "serverVersion")), "v")
		if strings.HasPrefix(version, "2.") {
			version = "2"
		} else {
			version = "1"
		}
	}
	return database.InfluxPreviewOptions{
		Version: version, Database: str(o, "database"), Table: str(o, "tableName"),
		Columns: compareStrings(o["columns"]), Where: str(o, "whereInput"), Order: str(o, "orderBy"),
		Limit: numberValue(o, "limit", 0), Offset: numberValue(o, "offset", 0),
		DefaultTime: boolValue(o, "injectDefaultTimeSeriesWhere"), Count: count,
		TagRenames: influxRenameMap(o["influxDbTagRenames"]), FieldRenames: influxRenameMap(o["influxDbFieldRenames"]),
	}
}
func influxRenameMap(value any) map[string]string {
	result := map[string]string{}
	for name, alias := range mapValue(map[string]any{"value": value}, "value") {
		if target, ok := alias.(string); ok {
			result[name] = target
		}
	}
	return result
}

func buildInfluxSQL(path string, o map[string]any) (any, bool, error) {
	switch path {
	case "/api/query/build-table-select-sql", "/api/query/build-data-grid-count-sql":
		result, err := database.BuildInfluxPreview(influxPreviewOptions(o, strings.Contains(path, "count")))
		return result, true, err
	case "/api/query/prepare-pagination-plan":
		source := str(o, "sql")
		plan := map[string]any{"sqlToExecute": source, "useAgentResultSession": false}
		preview, ok := database.ParseInfluxPreview(source)
		if !ok {
			// Arbitrary Flux/InfluxQL cannot be wrapped in a relational subquery.
			return plan, true, nil
		}
		pagination := mapValue(o, "pagination")
		limit, offset := numberValue(pagination, "limit", 100), numberValue(pagination, "offset", 0)
		if limit <= 0 || limit > maxTransferRows || offset < 0 {
			return nil, true, errors.New("Invalid pagination")
		}
		// Query mode pages the result of the original bounded preview. Table data
		// mode rebuilds its own query and therefore does not pass through this plan.
		if preview.Count {
			return plan, true, nil
		}
		if preview.Limit > 0 {
			bound := preview.Limit
			plan["exactQueryRowBound"] = bound
			if offset >= bound {
				preview.Empty = true
			} else if limit > bound-offset {
				limit = bound - offset
			}
		}
		preview.Offset += offset
		preview.Limit = limit
		paged, err := database.BuildInfluxPreview(preview)
		if err != nil {
			return nil, true, err
		}
		plan["sqlToExecute"], plan["pageSql"], plan["pageLimit"], plan["pageOffset"] = paged, paged, limit, offset
		if _, bounded := plan["exactQueryRowBound"]; !bounded {
			preview.Count, preview.Limit, preview.Offset = true, 0, 0
			plan["countSql"], err = database.BuildInfluxPreview(preview)
		}
		return plan, true, err
	case "/api/query/build-sorted-sql":
		preview, ok := database.ParseInfluxPreview(str(o, "originalSql"))
		if !ok || preview.Count || preview.Limit > 0 || preview.Offset > 0 {
			return map[string]any{"ok": false, "reason": "unsupported"}, true, nil
		}
		columns := compareStrings(o["resultColumns"])
		index := numberValue(o, "columnIndex", -1)
		if index < 0 || index >= len(columns) {
			return nil, true, errors.New("Sort column is out of range")
		}
		direction := strings.ToUpper(str(o, "direction"))
		if direction != "ASC" && direction != "DESC" {
			return nil, true, errors.New("Invalid sort direction")
		}
		preview.Order = quoteSQLName(columns[index], "influxdb") + " " + direction
		result, err := database.BuildInfluxPreview(preview)
		return map[string]any{"ok": err == nil, "sql": result}, true, err
	}
	return nil, false, nil
}
