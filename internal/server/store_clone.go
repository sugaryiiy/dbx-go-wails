package server

import "encoding/json"

// cloneStateValue preserves the JSON-shaped values exposed by the store without
// encoding and copying immutable strings, which can contain large SQL/cache data.
// Less common Go values still use JSON so typed slices and structs keep the same
// representation as values loaded from state.enc.
func cloneStateValue(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool, string, float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case map[string]any:
		if v == nil {
			return nil, nil
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			copy, err := cloneStateValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = copy
		}
		return out, nil
	case []any:
		if v == nil {
			return nil, nil
		}
		out := make([]any, len(v))
		for i, item := range v {
			copy, err := cloneStateValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = copy
		}
		return out, nil
	case []map[string]any:
		if v == nil {
			return nil, nil
		}
		out := make([]any, len(v))
		for i, item := range v {
			copy, err := cloneStateValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = copy
		}
		return out, nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var out any
		err = json.Unmarshal(data, &out)
		return out, err
	}
}

func cloneStateMap(value map[string]any) (map[string]any, error) {
	copy, err := cloneStateValue(value)
	if copy == nil || err != nil {
		return nil, err
	}
	return copy.(map[string]any), nil
}

func cloneStateRecords(records []map[string]any) ([]map[string]any, error) {
	if records == nil {
		return nil, nil
	}
	out := make([]map[string]any, len(records))
	for i, item := range records {
		copy, err := cloneStateMap(item)
		if err != nil {
			return nil, err
		}
		out[i] = copy
	}
	return out, nil
}

func cloneDiskState(state diskState) (diskState, error) {
	out := diskState{}
	for _, field := range []struct {
		value []map[string]any
		out   *[]map[string]any
	}{
		{state.Connections, &out.Connections},
		{state.History, &out.History},
		{state.SQLFiles, &out.SQLFiles},
		{state.SQLFolders, &out.SQLFolders},
	} {
		copy, err := cloneStateRecords(field.value)
		if err != nil {
			return diskState{}, err
		}
		*field.out = copy
	}
	var err error
	if out.Settings, err = cloneStateMap(state.Settings); err != nil {
		return diskState{}, err
	}
	if out.TableGroups, err = cloneStateMap(state.TableGroups); err != nil {
		return diskState{}, err
	}
	if out.Sidebar, err = cloneStateValue(state.Sidebar); err != nil {
		return diskState{}, err
	}
	return out, nil
}
