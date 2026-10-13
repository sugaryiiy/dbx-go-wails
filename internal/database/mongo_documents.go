package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const mongoMaxRows = 10000
const mongoMaxBytes = 32 << 20

type mongoDocumentResult struct {
	Documents         []any `json:"documents"`
	ExtendedDocuments []any `json:"extended_documents"`
	Total             int64 `json:"total"`
	TotalIsExact      bool  `json:"total_is_exact"`
}

func mongoDocument(text string) (bson.D, error) {
	if strings.TrimSpace(text) == "" {
		return bson.D{}, nil
	}
	if len(text) > 16<<20 {
		return nil, errors.New("MongoDB JSON exceeds 16 MiB")
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return nil, errors.New("MongoDB value must be a JSON object")
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &doc); err != nil {
		return nil, fmt.Errorf("invalid MongoDB Extended JSON: %w", err)
	}
	// The BSON parser accepts duplicate keys; reject them explicitly so policy
	// validation and MongoDB never disagree about which value is effective.
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := mongoUniqueJSON(dec); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing MongoDB JSON content")
	}
	return doc, nil
}

func mongoUniqueJSON(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for dec.More() {
			key, e := dec.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if seen[s] {
				return fmt.Errorf("duplicate MongoDB JSON key %s", s)
			}
			seen[s] = true
			if e = mongoUniqueJSON(dec); e != nil {
				return e
			}
		}
	} else if delim == '[' {
		for dec.More() {
			if err = mongoUniqueJSON(dec); err != nil {
				return err
			}
		}
	} else {
		return errors.New("invalid JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

func mongoArray(text string) (bson.A, error) {
	doc, err := mongoDocument(`{"values":` + text + `}`)
	if err != nil {
		return nil, err
	}
	arr, ok := doc[0].Value.(bson.A)
	if !ok {
		return nil, errors.New("MongoDB value must be a JSON array")
	}
	return arr, nil
}

func mongoGet(doc bson.D, key string) any {
	for _, item := range doc {
		if item.Key == key {
			return item.Value
		}
	}
	return nil
}
func mongoOptions(text string, allowed ...string) (bson.D, error) {
	doc, err := mongoDocument(text)
	if err != nil {
		return nil, err
	}
	for _, field := range doc {
		ok := false
		for _, key := range allowed {
			if field.Key == key {
				ok = true
				break
			}
		}
		if !ok {
			return nil, unsupported("MongoDB option " + field.Key)
		}
	}
	return doc, nil
}

// No server-side JavaScript, change streams, or output stages are accepted,
// including those nested inside lookup/facet/unionWith pipelines.
func mongoSafeRead(value any) error {
	switch v := value.(type) {
	case bson.D:
		for _, field := range v {
			switch field.Key {
			case "$where", "$function", "$accumulator", "$out", "$merge", "$changeStream":
				return unsupported("MongoDB query operator " + field.Key)
			}
			if err := mongoSafeRead(field.Value); err != nil {
				return err
			}
		}
	case bson.A:
		for _, item := range v {
			if err := mongoSafeRead(item); err != nil {
				return err
			}
		}
	case bson.JavaScript, bson.CodeWithScope:
		return unsupported("MongoDB server-side JavaScript")
	}
	return nil
}

func mongoRowLimit(p map[string]any, names ...string) (int64, error) {
	value := str(p, names...)
	if value == "" {
		return 200, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || n > mongoMaxRows {
		return 0, fmt.Errorf("MongoDB row limit must be between 0 and %d", mongoMaxRows)
	}
	if n == 0 {
		n = mongoMaxRows
	}
	return n, nil
}

func mongoCanonical(value any) (any, error) {
	encoded, err := bson.MarshalExtJSON(bson.D{{Key: "value", Value: value}}, true, false)
	if err != nil {
		return nil, err
	}
	var wrapper map[string]any
	dec := json.NewDecoder(strings.NewReader(string(encoded)))
	dec.UseNumber()
	if err = dec.Decode(&wrapper); err != nil {
		return nil, err
	}
	return wrapper["value"], nil
}

func mongoCanonicalRaw(value any) (json.RawMessage, error) {
	encoded, err := bson.MarshalExtJSON(bson.D{{Key: "value", Value: value}}, true, false)
	if err != nil {
		return nil, err
	}
	var wrapper map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &wrapper); err != nil {
		return nil, err
	}
	return wrapper["value"], nil
}

// Preserve BSON-only types in both views. Numeric int32/double and safe int64
// remain convenient in the grid; canonical copy/edit data retains all types.
func mongoDisplay(value any, field string) any {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 1 {
			for key, item := range v {
				s, ok := item.(string)
				if !ok {
					break
				}
				switch key {
				case "$numberInt":
					return json.Number(s)
				case "$numberDouble":
					if s != "NaN" && s != "Infinity" && s != "-Infinity" {
						return json.Number(s)
					}
				case "$numberLong":
					if n, e := strconv.ParseInt(s, 10, 64); e == nil && field != "_id" && n >= -9007199254740991 && n <= 9007199254740991 {
						return n
					}
				}
			}
		}
		result := map[string]any{}
		for key, item := range v {
			result[key] = mongoDisplay(item, key)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = mongoDisplay(item, "")
		}
		return result
	default:
		return value
	}
}

func mongoResult(values []any, total int64, exact bool) (mongoDocumentResult, error) {
	result := mongoDocumentResult{Documents: []any{}, ExtendedDocuments: []any{}, Total: total, TotalIsExact: exact}
	size := 0
	for _, value := range values {
		canonical, err := mongoCanonical(value)
		if err != nil {
			return result, err
		}
		encoded, err := mongoCanonicalRaw(value)
		if err != nil {
			return result, err
		}
		size += len(encoded)
		if size > mongoMaxBytes {
			return result, errors.New("MongoDB result exceeds 32 MiB; reduce the row limit or projection")
		}
		result.ExtendedDocuments = append(result.ExtendedDocuments, encoded)
		display := mongoDisplay(canonical, "")
		// BSON document IDs compare their field order. Keep the original wire
		// order rather than re-encoding their fields from an unordered Go map.
		if doc, ok := value.(bson.D); ok {
			if id, ok := mongoGet(doc, "_id").(bson.D); ok {
				rawID, err := mongoCanonicalRaw(id)
				if err != nil {
					return result, err
				}
				if fields, ok := display.(map[string]any); ok {
					fields["_id"] = rawID
				}
			}
		}
		result.Documents = append(result.Documents, display)
	}
	return result, nil
}

func mongoCursor(ctx context.Context, cursor *mongo.Cursor, limit int64) ([]any, bool, error) {
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = cursor.Close(closeCtx)
	}()
	values := []any{}
	size := 0
	for cursor.Next(ctx) {
		if int64(len(values)) >= limit {
			return values, false, nil
		}
		size += len(cursor.Current)
		if size > mongoMaxBytes {
			return nil, false, errors.New("MongoDB result exceeds 32 MiB; reduce the row limit or projection")
		}
		var doc bson.D
		if err := cursor.Decode(&doc); err != nil {
			return nil, false, err
		}
		values = append(values, doc)
	}
	return values, true, cursor.Err()
}

func mongoCollation(text string) (*options.Collation, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	doc, err := mongoOptions(text, "locale", "caseLevel", "caseFirst", "strength", "numericOrdering", "alternate", "maxVariable", "normalization", "backwards")
	if err != nil {
		return nil, err
	}
	encoded, err := bson.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var collation options.Collation
	if err = bson.Unmarshal(encoded, &collation); err != nil {
		return nil, err
	}
	if collation.Locale == "" {
		return nil, errors.New("MongoDB collation locale is required")
	}
	return &collation, nil
}

func (d *mongoDriver) readDocuments(ctx context.Context, db *mongo.Database, col *mongo.Collection, path string, p map[string]any) (any, error) {
	filter, err := mongoDocument(str(p, "filter", "filterJson"))
	if err != nil {
		return nil, err
	}
	if err = mongoSafeRead(filter); err != nil {
		return nil, err
	}
	if path == "/api/mongo/count-documents" || path == "/api/document-store/count-documents" {
		if mode := str(p, "mode"); mode != "" && mode != "accurate" {
			return nil, unsupported("MongoDB legacy count mode")
		}
		return col.CountDocuments(ctx, filter)
	}
	if path == "/api/mongo/distinct" {
		field := str(p, "field")
		if field == "" {
			return nil, errors.New("MongoDB distinct field is required")
		}
		var decoded bson.A
		if err = col.Distinct(ctx, field, filter).Decode(&decoded); err != nil {
			return nil, err
		}
		if len(decoded) > mongoMaxRows {
			return nil, errors.New("MongoDB distinct result exceeds 10000 values")
		}
		return mongoResult([]any(decoded), int64(len(decoded)), true)
	}
	limit, err := mongoRowLimit(p, "limit", "maxRows")
	if err != nil {
		return nil, err
	}
	if path == "/api/mongo/aggregate-documents" {
		pipeline, err := mongoArray(str(p, "pipelineJson"))
		if err != nil {
			return nil, err
		}
		if err = mongoSafeRead(pipeline); err != nil {
			return nil, err
		}
		for _, stage := range pipeline {
			doc, ok := stage.(bson.D)
			if !ok || len(doc) != 1 {
				return nil, errors.New("MongoDB aggregation stages must each have one operator")
			}
		}
		opts, err := mongoOptions(str(p, "optionsJson"), "allowDiskUse", "collation", "maxTimeMS")
		if err != nil {
			return nil, err
		}
		if value := mongoGet(opts, "maxTimeMS"); value != nil {
			var milliseconds int64
			switch n := value.(type) {
			case int32:
				milliseconds = int64(n)
			case int64:
				milliseconds = n
			default:
				return nil, errors.New("maxTimeMS must be an integer")
			}
			if milliseconds < 1 || milliseconds > 300000 {
				return nil, errors.New("maxTimeMS must be between 1 and 300000")
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(milliseconds)*time.Millisecond)
			defer cancel()
		}
		ao := options.Aggregate().SetBatchSize(100)
		if value := mongoGet(opts, "allowDiskUse"); value != nil {
			flag, ok := value.(bool)
			if !ok {
				return nil, errors.New("allowDiskUse must be a boolean")
			}
			ao.SetAllowDiskUse(flag)
		}
		if value := mongoGet(opts, "collation"); value != nil {
			raw, e := bson.MarshalExtJSON(value, false, false)
			if e != nil {
				return nil, e
			}
			collation, e := mongoCollation(string(raw))
			if e != nil {
				return nil, e
			}
			ao.SetCollation(collation)
		}
		// Limit the server result as well as client materialization. The extra row
		// tells the UI whether the displayed count is truncated.
		pipeline = append(pipeline, bson.D{{Key: "$limit", Value: limit + 1}})
		cursor, err := col.Aggregate(ctx, pipeline, ao)
		if err != nil {
			return nil, err
		}
		values, exact, err := mongoCursor(ctx, cursor, limit)
		if err != nil {
			return nil, err
		}
		return mongoResult(values, int64(len(values)), exact)
	}
	projection, err := mongoDocument(str(p, "projection"))
	if err != nil {
		return nil, err
	}
	if err = mongoSafeRead(projection); err != nil {
		return nil, err
	}
	sortDoc, err := mongoDocument(str(p, "sort"))
	if err != nil {
		return nil, err
	}
	if path == "/api/mongo/find-one" {
		opts, e := mongoOptions(str(p, "options"), "sort")
		if e != nil {
			return nil, e
		}
		if value := mongoGet(opts, "sort"); value != nil {
			var ok bool
			sortDoc, ok = value.(bson.D)
			if !ok {
				return nil, errors.New("sort must be an object")
			}
		}
		fo := options.FindOne().SetProjection(projection)
		if len(sortDoc) > 0 {
			fo.SetSort(sortDoc)
		}
		var doc bson.D
		err = col.FindOne(ctx, filter, fo).Decode(&doc)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return mongoResult(nil, 0, true)
		}
		if err != nil {
			return nil, err
		}
		return mongoResult([]any{doc}, 1, true)
	}
	skip := int64(0)
	if value := str(p, "skip"); value != "" {
		skip, err = strconv.ParseInt(value, 10, 64)
		if err != nil || skip < 0 {
			return nil, errors.New("MongoDB skip must be a nonnegative integer")
		}
	}
	if str(p, "cursor") != "" || boolean(p, "cursorPagination") {
		return nil, unsupported("MongoDB cursor pagination")
	}
	collation, err := mongoCollation(str(p, "collation"))
	if err != nil {
		return nil, err
	}
	if path == "/api/mongo/explain-find" {
		verbosity := str(p, "verbosity")
		if verbosity == "" {
			verbosity = "queryPlanner"
		}
		if verbosity != "queryPlanner" && verbosity != "executionStats" && verbosity != "allPlansExecution" {
			return nil, errors.New("invalid MongoDB explain verbosity")
		}
		find := bson.D{{Key: "find", Value: col.Name()}, {Key: "filter", Value: filter}, {Key: "projection", Value: projection}, {Key: "skip", Value: skip}, {Key: "limit", Value: limit}}
		if len(sortDoc) > 0 {
			find = append(find, bson.E{Key: "sort", Value: sortDoc})
		}
		if collation != nil {
			find = append(find, bson.E{Key: "collation", Value: collation})
		}
		var doc bson.D
		err = db.RunCommand(ctx, bson.D{{Key: "explain", Value: find}, {Key: "verbosity", Value: verbosity}}).Decode(&doc)
		if err != nil {
			return nil, err
		}
		canonical, err := mongoCanonical(doc)
		return mongoDisplay(canonical, ""), err
	}
	fo := options.Find().SetSkip(skip).SetLimit(limit).SetBatchSize(100).SetProjection(projection)
	if len(sortDoc) > 0 {
		fo.SetSort(sortDoc)
	}
	if collation != nil {
		fo.SetCollation(collation)
	}
	cursor, err := col.Find(ctx, filter, fo)
	if err != nil {
		return nil, err
	}
	values, _, err := mongoCursor(ctx, cursor, limit)
	if err != nil {
		return nil, err
	}
	co := options.Count()
	if collation != nil {
		co.SetCollation(collation)
	}
	total, err := col.CountDocuments(ctx, filter, co)
	if err != nil {
		return nil, err
	}
	return mongoResult(values, total, true)
}

func mongoID(id string) (any, error) {
	if strings.HasPrefix(id, "__dbx_mongo_string_id__") {
		var value string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(id, "__dbx_mongo_string_id__")), &value); err != nil {
			return nil, errors.New("invalid typed MongoDB string ID")
		}
		return value, nil
	}
	if strings.HasPrefix(strings.TrimSpace(id), "{") {
		doc, err := mongoDocument(`{"_id":` + id + `}`)
		if err != nil {
			return nil, err
		}
		return doc[0].Value, nil
	}
	if value, err := bson.ObjectIDFromHex(id); err == nil {
		return value, nil
	}
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return n, nil
	}
	if id == "true" {
		return true, nil
	}
	if id == "false" {
		return false, nil
	}
	if n, err := strconv.ParseFloat(id, 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
		return n, nil
	}
	if id == "" {
		return nil, errors.New("MongoDB document ID is required")
	}
	return id, nil
}

func mongoAffected(n int64) map[string]any { return map[string]any{"affected_rows": n} }

func (d *mongoDriver) writeDocuments(ctx context.Context, db *mongo.Database, col *mongo.Collection, path string, p map[string]any) (any, error) {
	if str(p, "routing") != "" {
		return nil, unsupported("MongoDB document routing")
	}
	if path == "/api/document-store/insert-document" {
		doc, err := mongoDocument(str(p, "docJson"))
		if err != nil {
			return nil, err
		}
		result, err := col.InsertOne(ctx, doc)
		if err != nil {
			return nil, err
		}
		if id, ok := result.InsertedID.(bson.ObjectID); ok {
			return id.Hex(), nil
		}
		id, err := mongoCanonical(result.InsertedID)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(id)
		return string(encoded), err
	}
	if path == "/api/mongo/insert-documents" {
		text := str(p, "docsJson")
		var docs []any
		if strings.HasPrefix(strings.TrimSpace(text), "[") {
			arr, err := mongoArray(text)
			if err != nil {
				return nil, err
			}
			docs = []any(arr)
		} else {
			doc, err := mongoDocument(text)
			if err != nil {
				return nil, err
			}
			docs = []any{doc}
		}
		if len(docs) > 10000 {
			return nil, errors.New("MongoDB insert exceeds 10000 documents")
		}
		for _, doc := range docs {
			if _, ok := doc.(bson.D); !ok {
				return nil, errors.New("each MongoDB document must be an object")
			}
		}
		if len(docs) == 0 {
			return mongoAffected(0), nil
		}
		result, err := col.InsertMany(ctx, docs)
		if err != nil {
			return nil, err
		}
		return mongoAffected(int64(len(result.InsertedIDs))), nil
	}
	filter, err := mongoDocument(str(p, "filterJson"))
	if err != nil {
		return nil, err
	}
	byID := path == "/api/document-store/update-document" || path == "/api/document-store/delete-document"
	if byID {
		id, err := mongoID(str(p, "id"))
		if err != nil {
			return nil, err
		}
		filter = bson.D{{Key: "_id", Value: id}}
	}
	if err = mongoSafeRead(filter); err != nil {
		return nil, err
	}
	if path == "/api/document-store/delete-document" || path == "/api/mongo/delete-documents" {
		var result *mongo.DeleteResult
		if !byID && boolean(p, "many") {
			result, err = col.DeleteMany(ctx, filter)
		} else {
			result, err = col.DeleteOne(ctx, filter)
		}
		if err != nil {
			return nil, err
		}
		if byID {
			return result.DeletedCount, nil
		}
		return mongoAffected(result.DeletedCount), nil
	}
	opts, err := mongoOptions(str(p, "optionsJson"), "upsert", "returnDocument", "returnNewDocument", "projection", "sort", "arrayFilters", "collation")
	if err != nil {
		return nil, err
	}
	if err = mongoSafeRead(opts); err != nil {
		return nil, err
	}
	if mongoGet(opts, "returnDocument") != nil && mongoGet(opts, "returnNewDocument") != nil {
		return nil, errors.New("choose returnDocument or returnNewDocument, not both")
	}
	upsert := false
	if value := mongoGet(opts, "upsert"); value != nil {
		var ok bool
		upsert, ok = value.(bool)
		if !ok {
			return nil, errors.New("upsert must be boolean")
		}
	}
	if path == "/api/mongo/find-one-and-delete" || path == "/api/mongo/find-one-and-update" || path == "/api/mongo/find-one-and-replace" {
		command := bson.D{{Key: "findAndModify", Value: col.Name()}, {Key: "query", Value: filter}}
		if path == "/api/mongo/find-one-and-delete" {
			if upsert {
				return nil, errors.New("delete does not support upsert")
			}
			command = append(command, bson.E{Key: "remove", Value: true})
		} else {
			text := str(p, "updateJson")
			if path == "/api/mongo/find-one-and-replace" {
				text = str(p, "replacementJson")
			}
			update, e := mongoDocument(text)
			if e != nil {
				return nil, e
			}
			if e = mongoSafeRead(update); e != nil {
				return nil, e
			}
			if path == "/api/mongo/find-one-and-update" && (len(update) == 0 || !strings.HasPrefix(update[0].Key, "$")) {
				return nil, errors.New("update must contain MongoDB update operators")
			}
			command = append(command, bson.E{Key: "update", Value: update}, bson.E{Key: "upsert", Value: upsert})
		}
		for _, item := range opts {
			switch item.Key {
			case "upsert":
			case "projection":
				command = append(command, bson.E{Key: "fields", Value: item.Value})
			case "returnDocument":
				s, ok := item.Value.(string)
				if !ok || (s != "before" && s != "after") {
					return nil, errors.New("returnDocument must be before or after")
				}
				command = append(command, bson.E{Key: "new", Value: s == "after"})
			case "returnNewDocument":
				flag, ok := item.Value.(bool)
				if !ok {
					return nil, errors.New("returnNewDocument must be boolean")
				}
				command = append(command, bson.E{Key: "new", Value: flag})
			default:
				command = append(command, item)
			}
		}
		var result bson.D
		if err = db.RunCommand(ctx, command).Decode(&result); err != nil {
			return nil, err
		}
		value := mongoGet(result, "value")
		if value == nil {
			return mongoResult(nil, 0, true)
		}
		return mongoResult([]any{value}, 1, true)
	}
	// Options for findAndModify must not be silently ignored on update/replace.
	for _, item := range opts {
		if item.Key != "upsert" {
			return nil, unsupported("MongoDB update/replace option " + item.Key)
		}
	}
	text := str(p, "updateJson")
	replace := path == "/api/mongo/replace-document"
	if replace {
		text = str(p, "replacementJson")
	}
	if byID {
		text = str(p, "docJson")
	}
	update, err := mongoDocument(text)
	if err != nil {
		return nil, err
	}
	if err = mongoSafeRead(update); err != nil {
		return nil, err
	}
	if byID {
		replace = len(update) == 0 || !strings.HasPrefix(update[0].Key, "$")
		if replace {
			withoutID := bson.D{}
			for _, item := range update {
				if item.Key != "_id" {
					withoutID = append(withoutID, item)
				}
			}
			update = withoutID
		}
	}
	var result *mongo.UpdateResult
	if replace {
		result, err = col.ReplaceOne(ctx, filter, update, options.Replace().SetUpsert(upsert))
	} else {
		if len(update) == 0 || !strings.HasPrefix(update[0].Key, "$") {
			return nil, errors.New("update must contain MongoDB update operators")
		}
		if boolean(p, "many") {
			result, err = col.UpdateMany(ctx, filter, update, options.UpdateMany().SetUpsert(upsert))
		} else {
			result, err = col.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(upsert))
		}
	}
	if err != nil {
		return nil, err
	}
	if byID {
		if result.MatchedCount == 0 {
			return nil, errors.New("no MongoDB document matched the selected ID")
		}
		return result.ModifiedCount, nil
	}
	return mongoAffected(result.ModifiedCount + result.UpsertedCount), nil
}
