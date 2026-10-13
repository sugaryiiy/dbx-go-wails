package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sugaryiiy/dbx-go-wails/internal/database"
	"github.com/sugaryiiy/dbx-go-wails/internal/mcpbridge"
)

var mcpObjectTypes = []string{"TABLE", "VIEW", "MATERIALIZED_VIEW", "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT", "SEQUENCE"}

func mcpObjectToolDefinitions() []mcpbridge.Tool {
	text := func() map[string]any { return map[string]any{"type": "string"} }
	enum := func(values []string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	selector := func() map[string]any {
		return map[string]any{"connection_id": text(), "connection_name": text(), "database": text(), "schema": text()}
	}
	tool := func(name, description string, props map[string]any, required ...string) mcpbridge.Tool {
		if required == nil {
			required = []string{}
		}
		return mcpbridge.Tool{Name: name, Description: description, ReadOnly: true, InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}
	}
	tools := []mcpbridge.Tool{}
	for _, spec := range []struct{ name, description string }{
		{"dbx_list_objects", "List supported database object names and identities, including routines, triggers and scheduled events. Returns bounded objects with total/offset/limit/truncated; default limit 100, maximum 1000. Body SQL is omitted."},
		{"dbx_list_routines", "List function/procedure names and PostgreSQL identity signatures without requiring access to their bodies (MySQL/MariaDB and PostgreSQL). Returns objects/total/offset/limit/truncated; default limit 100, maximum 1000."},
		{"dbx_list_events", "List MySQL/MariaDB scheduled events within the permitted database. These are distinct from PostgreSQL event triggers. Returns objects/total/offset/limit/truncated; default limit 100, maximum 1000."},
		{"dbx_list_triggers", "List SQLite/MySQL/MariaDB/PostgreSQL trigger identities, optionally for one relation_name. PostgreSQL trigger names can repeat on different tables. Returns objects/total/offset/limit/truncated; default limit 100, maximum 1000."},
	} {
		p := selector()
		p["filter"] = text()
		p["limit"] = integer(1, 1000)
		p["offset"] = integer(0, 2147483647)
		p["all_schemas"] = map[string]any{"type": "boolean"}
		switch spec.name {
		case "dbx_list_objects":
			p["object_types"] = map[string]any{"type": "array", "items": enum(mcpObjectTypes), "maxItems": len(mcpObjectTypes)}
		case "dbx_list_routines":
			p["routine_type"] = enum([]string{"FUNCTION", "PROCEDURE"})
		case "dbx_list_triggers":
			p["relation_name"] = text()
		}
		tools = append(tools, tool(spec.name, spec.description, p))
	}
	for _, name := range []string{"dbx_get_object_source", "dbx_get_routine_source", "dbx_get_event_info"} {
		p := selector()
		p["name"] = text()
		p["source_char_offset"] = integer(0, 2147483647)
		p["source_char_limit"] = integer(1, 65536)
		description := "Read MySQL/MariaDB scheduled event status, schedule and definition. Definition is redacted and bounded to 65536 characters by default; source_char_offset/source_char_limit select a window. Never schedules or executes the event."
		required := []string{"name"}
		if name != "dbx_get_event_info" {
			p["signature"] = text()
			description = "Read one object's CREATE source without executing it. Select PostgreSQL overloads with their exact identity signature (empty string selects zero arguments), and duplicate trigger names with relation_name. Source is redacted and bounded to 65536 characters by default; source_char_offset/source_char_limit select a window."
			if name == "dbx_get_routine_source" {
				p["routine_type"] = enum([]string{"FUNCTION", "PROCEDURE"})
				required = append(required, "routine_type")
				description = "Read a MySQL/MariaDB/PostgreSQL function or procedure CREATE source without calling it. Use exact PostgreSQL identity signature for overloads (empty string selects zero arguments). Source is redacted and bounded to 65536 characters by default; source_char_offset/source_char_limit select a window."
			} else {
				p["object_type"] = enum(mcpObjectTypes)
				p["relation_name"] = text()
				required = append(required, "object_type")
			}
		}
		tools = append(tools, tool(name, description, p, required...))
	}
	return tools
}

func mcpSchemaAllowsString(spec map[string]any, value string) bool {
	values, ok := spec["enum"].([]string)
	if !ok {
		return true
	}
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

func mcpValidateObjectToolArgs(name string, args map[string]any, scope mcpScope) error {
	switch name {
	case "dbx_list_objects", "dbx_list_routines", "dbx_list_events", "dbx_list_triggers":
		if boolValue(args, "all_schemas") && str(args, "schema") != "" {
			return errors.New("all_schemas cannot be combined with schema")
		}
	case "dbx_get_object_source", "dbx_get_routine_source":
		objectType := str(args, "object_type")
		if name == "dbx_get_routine_source" {
			objectType = str(args, "routine_type")
		}
		if _, exists := args["signature"]; exists && objectType != "FUNCTION" && objectType != "PROCEDURE" {
			return errors.New("signature can only select a FUNCTION or PROCEDURE")
		}
		if _, exists := args["relation_name"]; exists && objectType != "TRIGGER" {
			return errors.New("relation_name can only select a TRIGGER")
		}
		if !mcpObjectTypeSupported(str(scope.config, "db_type"), objectType) {
			return fmt.Errorf("%s source is not supported by this database driver", objectType)
		}
	}
	kind := str(scope.config, "db_type")
	if (name == "dbx_list_events" || name == "dbx_get_event_info") && kind != "mysql" && kind != "mariadb" {
		return errors.New("Scheduled events are only supported by MySQL/MariaDB; PostgreSQL event triggers are a different object type")
	}
	if (name == "dbx_list_routines" || name == "dbx_get_routine_source") && kind == "sqlite" {
		return errors.New("SQLite has no stored FUNCTION or PROCEDURE catalog")
	}
	return nil
}

func mcpObjectTypeSupported(kind, objectType string) bool {
	switch kind {
	case "sqlite":
		return objectType == "TABLE" || objectType == "VIEW" || objectType == "TRIGGER"
	case "mysql", "mariadb":
		return objectType == "TABLE" || objectType == "VIEW" || objectType == "FUNCTION" || objectType == "PROCEDURE" || objectType == "TRIGGER" || objectType == "EVENT"
	case "postgres":
		return objectType != "EVENT"
	}
	return false
}

func mcpSchemaNames(value any) ([]string, error) {
	switch schemas := value.(type) {
	case []string:
		return schemas, nil
	case []map[string]any:
		names := make([]string, 0, len(schemas))
		for _, schema := range schemas {
			names = append(names, str(schema, "name"))
		}
		return names, nil
	default:
		return nil, errors.New("Database driver returned an invalid schema catalog")
	}
}

func mcpCatalogObjects(ctx context.Context, manager *database.Manager, req map[string]any, all bool) ([]map[string]any, error) {
	if !all {
		value, err := manager.Handle(ctx, "/api/schema/objects", req)
		if err != nil {
			return nil, err
		}
		objects, ok := value.([]map[string]any)
		if !ok {
			return nil, errors.New("Database driver returned an invalid object catalog")
		}
		return objects, nil
	}
	value, err := manager.Handle(ctx, "/api/schema/schemas", req)
	if err != nil {
		return nil, err
	}
	schemas, err := mcpSchemaNames(value)
	if err != nil {
		return nil, err
	}
	objects := []map[string]any{}
	for _, schema := range schemas {
		r := cloneMap(req)
		r["schema"] = schema
		rows, err := mcpCatalogObjects(ctx, manager, r, false)
		if err != nil {
			return nil, err
		}
		objects = append(objects, rows...)
	}
	return objects, nil
}

func (s *Server) mcpObjectMetadata(ctx context.Context, manager *database.Manager, name string, args map[string]any, scope mcpScope, req map[string]any) (any, error) {
	switch name {
	case "dbx_get_object_source", "dbx_get_routine_source":
		objectType := str(args, "object_type")
		if name == "dbx_get_routine_source" {
			objectType = str(args, "routine_type")
		}
		req["object_type"] = objectType
		req["filter"] = str(args, "name")
		objects, err := mcpCatalogObjects(ctx, manager, req, false)
		if err != nil {
			return nil, err
		}
		matches := []map[string]any{}
		for _, object := range objects {
			if str(object, "name") != str(args, "name") || str(object, "object_type") != objectType {
				continue
			}
			if signature, exists := args["signature"]; exists && str(object, "signature") != signature.(string) {
				continue
			}
			if relation := str(args, "relation_name"); relation != "" && str(object, "parent_name") != relation {
				continue
			}
			matches = append(matches, object)
		}
		if len(matches) == 0 {
			return nil, errors.New("Object does not exist or is not visible in the selected database/schema with that identity")
		}
		if len(matches) > 1 {
			return nil, errors.New("Object name is ambiguous; provide its routine signature or trigger relation_name")
		}
		req["table"] = str(args, "name")
		delete(req, "filter")
		if signature, exists := args["signature"]; exists {
			req["signature"] = signature
		}
		if relation := str(args, "relation_name"); relation != "" {
			req["relation_name"] = relation
		}
		value, err := manager.Handle(ctx, "/api/schema/object-source", req)
		if err != nil {
			return nil, err
		}
		out := cloneMap(value.(map[string]any))
		out["editable"] = false
		if signature, exists := matches[0]["signature"]; exists {
			out["signature"] = signature
		}
		if parent := str(matches[0], "parent_name"); parent != "" {
			out["relation_name"] = parent
		}
		s.mcpSourceWindow(out, "source", "source_truncated", args, scope.config)
		return out, nil
	case "dbx_get_event_info":
		req["table"] = str(args, "name")
		value, err := manager.Handle(ctx, "/api/schema/event-info", req)
		if err != nil {
			return nil, err
		}
		out := cloneMap(value.(map[string]any))
		for key, value := range out {
			if text, ok := value.(string); ok && key != "event_definition" && !mcpObjectIdentityField(key) {
				out[key], _ = mcpBoundText(s.mcpRedact(text, scope.config), 4096)
			}
		}
		s.mcpSourceWindow(out, "event_definition", "event_definition_truncated", args, scope.config)
		return out, nil
	}
	req["filter"] = str(args, "filter")
	switch name {
	case "dbx_list_objects":
		if types, ok := args["object_types"].([]any); ok {
			values := make([]string, 0, len(types))
			for _, objectType := range types {
				values = append(values, objectType.(string))
			}
			req["object_types"] = strings.Join(values, ",")
		}
	case "dbx_list_routines":
		req["object_types"] = "FUNCTION,PROCEDURE"
		if routineType := str(args, "routine_type"); routineType != "" {
			req["object_types"] = routineType
		}
	case "dbx_list_events":
		req["object_types"] = "EVENT"
	case "dbx_list_triggers":
		req["object_types"] = "TRIGGER"
	}
	objects, err := mcpCatalogObjects(ctx, manager, req, boolValue(args, "all_schemas"))
	if err != nil {
		return nil, err
	}
	if name == "dbx_list_triggers" && str(args, "relation_name") != "" {
		filtered := []map[string]any{}
		for _, object := range objects {
			if str(object, "parent_name") == str(args, "relation_name") {
				filtered = append(filtered, object)
			}
		}
		objects = filtered
	}
	sort.SliceStable(objects, func(i, j int) bool {
		for _, key := range []string{"schema", "name", "object_type", "signature", "parent_name"} {
			left, right := str(objects[i], key), str(objects[j], key)
			if left != right {
				return left < right
			}
		}
		return false
	})
	total := len(objects)
	offset, limit := mcpInteger(args["offset"], 0), mcpInteger(args["limit"], 100)
	start := min(offset, total)
	end := start + min(limit, total-start)
	page := []map[string]any{}
	// Keep identities intact so they can be passed back to source tools. Bound
	// the complete response by returning fewer rows, rather than truncating
	// signatures or names that happen to contain a saved login name.
	remainingBytes := (4 << 20) - 512
	for _, object := range objects[start:end] {
		out := map[string]any{}
		for _, key := range []string{"name", "schema", "object_type", "table_type", "signature", "parent_schema", "parent_name", "comment", "created_at", "updated_at", "event", "timing", "enabled"} {
			if value, exists := object[key]; exists {
				if text, ok := value.(string); ok && !mcpObjectIdentityField(key) {
					value, _ = mcpBoundText(s.mcpRedact(text, scope.config), 4096)
				}
				out[key] = value
			}
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return nil, errors.New("Database object metadata cannot be encoded")
		}
		rowBytes := len(encoded) + 1
		if rowBytes > remainingBytes {
			if len(page) == 0 {
				return nil, errors.New("One database object identity exceeds the MCP 4 MiB response budget")
			}
			break
		}
		remainingBytes -= rowBytes
		page = append(page, out)
	}
	out := map[string]any{"objects": page, "total": total, "offset": offset, "limit": limit, "truncated": start+len(page) < total}
	if start+len(page) < total {
		out["next_offset"] = start + len(page)
	}
	return out, nil
}

func mcpObjectIdentityField(key string) bool {
	switch key {
	case "name", "schema", "object_type", "table_type", "signature", "parent_schema", "parent_name":
		return true
	}
	return false
}

func (s *Server) mcpSourceWindow(out map[string]any, key, truncatedKey string, args, config map[string]any) {
	// Redact the entire text before slicing so a secret split across a window
	// boundary cannot be reconstructed by requesting adjacent pages.
	source := []rune(s.mcpRedact(str(out, key), config))
	offset, limit := mcpInteger(args["source_char_offset"], 0), mcpInteger(args["source_char_limit"], 65536)
	start := min(offset, len(source))
	end := start + min(limit, len(source)-start)
	out[key] = string(source[start:end])
	out["source_char_offset"] = offset
	out["source_char_limit"] = limit
	out[truncatedKey] = end < len(source)
	if end < len(source) {
		out["next_source_char_offset"] = end
	}
}
