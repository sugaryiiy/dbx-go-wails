package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxTabCacheEntryBytes = 8 << 20

func (s *Server) handleAppCache(w http.ResponseWriter, r *http.Request, p map[string]any) bool {
	if s.handleDocsAnnotations(w, r, p) {
		return true
	}
	route := strings.TrimSuffix(r.URL.Path, "/")
	var result any
	var err error
	switch {
	case route == "/api/schema/cache" && r.Method == "GET":
		key := str(p, "cache_key")
		if key == "" {
			key = str(p, "cacheKey")
		}
		result = s.schemaCacheEntry(key)
	case route == "/api/schema/cache" && r.Method == "POST":
		key := str(p, "cacheKey")
		if key == "" || len(key) > 4096 {
			err = errors.New("cacheKey is required and must be at most 4096 bytes")
			break
		}
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["schema.cache"])
			items[key] = p["payload"]
			st.Settings["schema.cache"] = items
			return nil
		})
	case route == "/api/schema/cache-prefix" && r.Method == "DELETE":
		prefix := str(p, "prefix")
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["schema.cache"])
			for k := range items {
				if strings.HasPrefix(k, prefix) {
					delete(items, k)
				}
			}
			st.Settings["schema.cache"] = items
			return nil
		})
	case route == "/api/tab-runtime-cache" && r.Method == "POST":
		key := str(p, "key")
		if key == "" || len(key) > 4096 {
			err = errors.New("key is required and must be at most 4096 bytes")
			break
		}
		payload, e := base64.StdEncoding.DecodeString(str(p, "payloadBase64"))
		if e != nil {
			err = errors.New("Invalid base64 cache payload")
			break
		}
		if len(payload) > maxTabCacheEntryBytes {
			err = errors.New("Tab cache entry exceeds 8 MiB")
			break
		}
		rows, cols := integer(p["rowCount"], -1), integer(p["columnCount"], -1)
		if rows < 0 || cols < 0 {
			err = errors.New("Row and column counts must be nonnegative")
			break
		}
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["tab.cache"])
			now := time.Now().UnixMilli()
			created := any(now)
			if old, ok := items[key].(map[string]any); ok {
				created = old["createdAt"]
			}
			items[key] = map[string]any{"key": key, "payloadBase64": base64.StdEncoding.EncodeToString(payload), "rowCount": rows, "columnCount": cols, "byteSize": len(payload), "createdAt": created, "lastAccessedAt": now, "updatedAt": time.Now().UTC().Format(time.RFC3339Nano), "ownerId": p["ownerId"]}
			st.Settings["tab.cache"] = items
			return nil
		})
	case route == "/api/tab-runtime-cache" && r.Method == "GET":
		result = s.tabCacheEntry(str(p, "key"))
	case route == "/api/tab-runtime-cache" && r.Method == "DELETE":
		key := str(p, "key")
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["tab.cache"])
			delete(items, key)
			st.Settings["tab.cache"] = items
			return nil
		})
	case route == "/api/tab-runtime-cache/metadata" && r.Method == "GET":
		items := s.tabCacheMetadata()
		sort.Slice(items, func(i, j int) bool { return str(items[i], "key") < str(items[j], "key") })
		result = items
	case route == "/api/tab-runtime-cache/owner" && r.Method == "DELETE":
		owner := str(p, "ownerId")
		if owner == "" {
			owner = str(p, "owner_id")
		}
		if owner == "" {
			err = errors.New("ownerId is required")
			break
		}
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["tab.cache"])
			count := 0
			for k, v := range items {
				if str(appObject(v), "ownerId") == owner {
					delete(items, k)
					count++
				}
			}
			st.Settings["tab.cache"] = items
			result = count
			return nil
		})
	case route == "/api/tab-runtime-cache/prune" && r.Method == "POST":
		req := p
		if nested, ok := p["request"].(map[string]any); ok {
			req = nested
		}
		err = s.update(func(st *diskState) error {
			items := appObject(st.Settings["tab.cache"])
			result = pruneAppCache(items, req, time.Now().UnixMilli())
			st.Settings["tab.cache"] = items
			return nil
		})
	default:
		return false
	}
	if err != nil {
		writeError(w, 400, err.Error())
	} else {
		writeJSON(w, 200, result)
	}
	return true
}
func pruneAppCache(items map[string]any, p map[string]any, now int64) map[string]any {
	live := map[string]bool{}
	if values, ok := p["liveKeys"].([]any); ok {
		for _, value := range values {
			if key, ok := value.(string); ok {
				live[key] = true
			}
		}
	}
	entries := []map[string]any{}
	total := 0
	for key, raw := range items {
		v := appObject(raw)
		v["key"] = key
		entries = append(entries, v)
		total += integer(v["byteSize"], 0)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := integer(entries[i]["lastAccessedAt"], 0), integer(entries[j]["lastAccessedAt"], 0)
		if a == b {
			return str(entries[i], "key") < str(entries[j], "key")
		}
		return a < b
	})
	deleted, deletedBytes, orphans := 0, 0, 0
	grace := integer(p["orphanGraceMs"], 0)
	if grace < 0 {
		grace = 0
	}
	maxBytes := integer(p["maxBytes"], 0)
	if maxBytes < 0 {
		maxBytes = 0
	}
	remove := func(v map[string]any) {
		delete(items, str(v, "key"))
		size := integer(v["byteSize"], 0)
		total -= size
		deletedBytes += size
		deleted++
	}
	for _, v := range entries {
		key := str(v, "key")
		if live[key] {
			continue
		}
		orphan := now-int64(integer(v["createdAt"], 0)) >= int64(grace)
		age := false
		if limit, ok := p["maxAgeMs"]; ok && limit != nil {
			maxAge := integer(limit, 0)
			if maxAge < 0 {
				maxAge = 0
			}
			age = now-int64(integer(v["lastAccessedAt"], 0)) >= int64(maxAge)
		}
		if orphan || age {
			remove(v)
			if orphan {
				orphans++
			}
		}
	}
	for _, v := range entries {
		if total <= maxBytes {
			break
		}
		key := str(v, "key")
		if live[key] || items[key] == nil {
			continue
		}
		remove(v)
	}
	return map[string]any{"deletedEntries": deleted, "deletedBytes": deletedBytes, "orphanDeletions": orphans, "remainingEntries": len(items), "remainingBytes": total}
}
