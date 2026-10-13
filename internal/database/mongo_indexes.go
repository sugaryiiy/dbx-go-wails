package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func mongoJSON(value any) (string, error) {
	canonical, err := mongoCanonical(value)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(mongoDisplay(canonical, ""))
	return string(data), err
}

func (d *mongoDriver) runCommand(ctx context.Context, db *mongo.Database, p map[string]any, locked bool) (any, error) {
	command, err := mongoDocument(str(p, "commandJson"))
	if err != nil {
		return nil, err
	}
	if len(command) == 0 {
		return nil, errors.New("MongoDB command is required")
	}
	if command[0].Key == "create" {
		if locked {
			return nil, errors.New("connection is read-only or production-locked; unlock writes before modifying MongoDB")
		}
		name, ok := command[0].Value.(string)
		if !ok {
			return nil, errors.New("MongoDB create requires a collection name")
		}
		if err = mongoNamespace(name, false); err != nil {
			return nil, err
		}
		encoded, e := bson.MarshalExtJSON(command[1:], false, false)
		if e != nil {
			return nil, e
		}
		if _, e = d.createCollection(ctx, db, name, map[string]any{"optionsJson": string(encoded)}); e != nil {
			return nil, e
		}
		return mongoResult([]any{bson.D{{Key: "ok", Value: 1}}}, 1, true)
	}
	allowed := map[string][]string{
		"ping": {}, "hello": {}, "buildInfo": {}, "buildinfo": {},
		"listDatabases": {"nameOnly", "authorizedDatabases", "filter"},
		"dbStats":       {"scale", "freeStorage"}, "dbstats": {"scale", "freeStorage"},
		"collStats": {"scale"}, "collstats": {"scale"},
	}
	fields, ok := allowed[command[0].Key]
	if !ok {
		return nil, unsupported("MongoDB run-command " + command[0].Key + "; use the dedicated document APIs")
	}
	for _, field := range command[1:] {
		found := false
		for _, name := range fields {
			if name == field.Key {
				found = true
				break
			}
		}
		if !found {
			return nil, unsupported("MongoDB command option " + field.Key)
		}
	}
	if err = mongoSafeRead(command); err != nil {
		return nil, err
	}
	if command[0].Key == "listDatabases" {
		db = d.client.Database("admin")
	}
	var doc bson.D
	if err = db.RunCommand(ctx, command).Decode(&doc); err != nil {
		return nil, err
	}
	return mongoResult([]any{doc}, 1, true)
}

func (d *mongoDriver) createCollection(ctx context.Context, db *mongo.Database, name string, p map[string]any) (any, error) {
	opts, err := mongoOptions(str(p, "optionsJson"), "capped", "size", "max", "validator", "validationLevel", "validationAction", "collation")
	if err != nil {
		return nil, err
	}
	if err = mongoSafeRead(opts); err != nil {
		return nil, err
	}
	command := append(bson.D{{Key: "create", Value: name}}, opts...)
	return nil, db.RunCommand(ctx, command).Err()
}

func mongoIndexSpecs(ctx context.Context, col *mongo.Collection) ([]map[string]any, error) {
	cursor, err := col.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	values, exact, err := mongoCursor(ctx, cursor, 10000)
	if err != nil {
		return nil, err
	}
	if !exact {
		return nil, errors.New("MongoDB index listing exceeds 10000 entries")
	}
	result := []map[string]any{}
	for _, value := range values {
		doc := value.(bson.D)
		name, _ := mongoGet(doc, "name").(string)
		keys := []map[string]any{}
		keyDoc, _ := mongoGet(doc, "key").(bson.D)
		for _, key := range keyDoc {
			keys = append(keys, map[string]any{"field": key.Key, "direction": fmt.Sprint(key.Value)})
		}
		flag := func(key string) bool { b, _ := mongoGet(doc, key).(bool); return b }
		spec := map[string]any{"name": name, "keys": keys, "is_unique": flag("unique") || name == "_id_", "is_primary": name == "_id_", "is_sparse": flag("sparse"), "expire_after_seconds": mongoGet(doc, "expireAfterSeconds"), "partial_filter_expression": nil, "background": flag("background"), "bucket_size": mongoGet(doc, "bucketSize"), "hidden": flag("hidden"), "properties_complete": true, "extra_options": nil}
		if filter := mongoGet(doc, "partialFilterExpression"); filter != nil {
			s, e := mongoJSON(filter)
			if e != nil {
				return nil, e
			}
			spec["partial_filter_expression"] = s
		}
		extra := bson.D{}
		for _, field := range doc {
			switch field.Key {
			case "name", "key", "v", "ns", "unique", "sparse", "expireAfterSeconds", "partialFilterExpression", "background", "bucketSize", "hidden":
			default:
				extra = append(extra, field)
			}
		}
		if len(extra) > 0 {
			s, e := mongoJSON(extra)
			if e != nil {
				return nil, e
			}
			spec["extra_options"] = s
		}
		result = append(result, spec)
	}
	return result, nil
}

func (d *mongoDriver) handleIndexes(ctx context.Context, db *mongo.Database, col *mongo.Collection, path string, p map[string]any) (any, error) {
	if path == "/api/mongo/list-index-specs" {
		return mongoIndexSpecs(ctx, col)
	}
	if path == "/api/mongo/collection-stats" {
		scale := integer(p, 1, "scale")
		if scale < 1 {
			return nil, errors.New("MongoDB stats scale must be positive")
		}
		var result bson.D
		if err := db.RunCommand(ctx, bson.D{{Key: "collStats", Value: col.Name()}, {Key: "scale", Value: scale}}).Decode(&result); err != nil {
			return nil, err
		}
		stats := map[string]any{}
		for _, key := range []string{"count", "size", "avgObjSize", "storageSize", "totalIndexSize", "nindexes"} {
			value := mongoGet(result, key)
			if value == nil {
				value = int32(0)
			}
			canonical, err := mongoCanonical(value)
			if err != nil {
				return nil, err
			}
			stats[key] = mongoDisplay(canonical, "")
		}
		return stats, nil
	}
	if path == "/api/mongo/create-index" {
		keys, err := mongoDocument(str(p, "keysJson"))
		if err != nil {
			return nil, err
		}
		if len(keys) == 0 {
			return nil, errors.New("MongoDB index keys are required")
		}
		opts, err := mongoOptions(str(p, "optionsJson"), "name", "unique", "sparse", "expireAfterSeconds", "partialFilterExpression", "hidden", "background", "collation", "weights", "default_language", "language_override", "textIndexVersion", "2dsphereIndexVersion", "bits", "min", "max", "wildcardProjection")
		if err != nil {
			return nil, err
		}
		if err = mongoSafeRead(opts); err != nil {
			return nil, err
		}
		name := ""
		if value := mongoGet(opts, "name"); value != nil {
			var ok bool
			name, ok = value.(string)
			if !ok || name == "" {
				return nil, errors.New("MongoDB index name must be a nonempty string")
			}
		}
		if name == "" {
			parts := []string{}
			for _, key := range keys {
				parts = append(parts, key.Key+"_"+fmt.Sprint(key.Value))
			}
			name = strings.Join(parts, "_")
			opts = append(opts, bson.E{Key: "name", Value: name})
		}
		index := append(bson.D{{Key: "key", Value: keys}}, opts...)
		if err = db.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: col.Name()}, {Key: "indexes", Value: bson.A{index}}}).Err(); err != nil {
			return nil, err
		}
		return map[string]any{"name": name}, nil
	}
	if path == "/api/mongo/drop-indexes" {
		text := str(p, "indexesJson")
		if text == "" {
			text = `"*"`
		}
		doc, err := mongoDocument(`{"index":` + text + `}`)
		if err != nil {
			return nil, err
		}
		index := doc[0].Value
		validate := func(value any) error {
			s, ok := value.(string)
			if !ok || s == "" || s == "_id_" {
				return errors.New("drop-indexes requires index names other than _id_")
			}
			return nil
		}
		switch value := index.(type) {
		case string:
			if err = validate(value); err != nil {
				return nil, err
			}
			if boolean(p, "single") && value == "*" {
				return nil, errors.New("single index drop cannot use wildcard")
			}
		case bson.A:
			if boolean(p, "single") || len(value) == 0 {
				return nil, errors.New("invalid index name array")
			}
			for _, name := range value {
				if err = validate(name); err != nil {
					return nil, err
				}
				if name == "*" {
					return nil, errors.New("wildcard cannot be mixed with index names")
				}
			}
		default:
			return nil, errors.New("drop-indexes requires a name or array of names")
		}
		before, err := mongoIndexSpecs(ctx, col)
		if err != nil {
			return nil, err
		}
		if err = db.RunCommand(ctx, bson.D{{Key: "dropIndexes", Value: col.Name()}, {Key: "index", Value: index}}).Err(); err != nil {
			return nil, err
		}
		after, err := mongoIndexSpecs(ctx, col)
		if err != nil {
			return nil, err
		}
		remaining := map[string]bool{}
		for _, spec := range after {
			remaining[spec["name"].(string)] = true
		}
		dropped := []string{}
		for _, spec := range before {
			name := spec["name"].(string)
			if !remaining[name] {
				dropped = append(dropped, name)
			}
		}
		return map[string]any{"dropped_names": dropped, "affected_rows": len(dropped)}, nil
	}
	return nil, unsupported(path)
}
