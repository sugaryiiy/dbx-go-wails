package database

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (d *mongoDriver) schema(ctx context.Context, col *mongo.Collection, path string) (any, error) {
	if path == "/api/schema/indexes" {
		specs, err := mongoIndexSpecs(ctx, col)
		if err != nil {
			return nil, err
		}
		result := []map[string]any{}
		for _, spec := range specs {
			columns := []string{}
			description := []string{}
			for _, key := range spec["keys"].([]map[string]any) {
				columns = append(columns, key["field"].(string))
				description = append(description, fmt.Sprint(key["field"])+": "+fmt.Sprint(key["direction"]))
			}
			result = append(result, map[string]any{"name": spec["name"], "columns": columns, "is_primary": spec["is_primary"], "is_unique": spec["is_unique"], "filter": spec["partial_filter_expression"], "index_type": strings.Join(description, ", "), "key_is_expression": []bool{}, "column_opclasses": []string{}, "key_options": []string{}, "constraint_backed": false})
		}
		return result, nil
	}
	cursor, err := col.Find(ctx, bson.D{}, options.Find().SetLimit(100).SetBatchSize(100))
	if err != nil {
		return nil, err
	}
	values, _, err := mongoCursor(ctx, cursor, 100)
	if err != nil {
		return nil, err
	}
	fields := map[string]map[string]bool{}
	for _, value := range values {
		for _, field := range value.(bson.D) {
			if fields[field.Key] == nil {
				if len(fields) >= 512 {
					continue
				}
				fields[field.Key] = map[string]bool{}
			}
			fields[field.Key][mongoBSONType(field.Value)] = true
		}
	}
	names := []string{}
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	result := []map[string]any{}
	for _, name := range names {
		types := []string{}
		for kind := range fields[name] {
			types = append(types, kind)
		}
		sort.Strings(types)
		result = append(result, map[string]any{"name": name, "data_type": strings.Join(types, " | "), "is_nullable": true, "column_default": nil, "is_primary_key": name == "_id", "is_unique": name == "_id", "extra": fmt.Sprintf("sampled from %d document(s); schema may be incomplete", len(values))})
	}
	return result, nil
}

func mongoBSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case int32, int64, float64:
		return "number"
	case bson.D:
		return "object"
	case bson.A:
		return "array"
	case bson.ObjectID:
		return "objectId"
	case bson.DateTime:
		return "date"
	case bson.Binary:
		return "binary"
	case bson.Decimal128:
		return "decimal128"
	case bson.Regex:
		return "regex"
	case bson.Timestamp:
		return "timestamp"
	case bson.JavaScript, bson.CodeWithScope:
		return "javascript"
	case bson.MinKey:
		return "minKey"
	case bson.MaxKey:
		return "maxKey"
	default:
		return "unknown"
	}
}
