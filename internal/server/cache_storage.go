package server

import "time"

func (s *Server) schemaCacheEntry(key string) any {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	value, _ := cloneStateValue(appObject(s.state.Settings["schema.cache"])[key])
	return value
}

func cacheAccessTime(value any) int64 {
	if n, ok := value.(int64); ok {
		return n
	}
	return int64(integer(value, 0))
}

// Recency is best effort metadata: reading a cached result must not rewrite and
// sync the entire application state. The next durable update or Close saves it.
func (s *Server) tabCacheEntry(key string) map[string]any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	entry, ok := appObject(s.state.Settings["tab.cache"])[key].(map[string]any)
	if !ok || entry == nil {
		return nil
	}
	if now := time.Now().UnixMilli(); now > cacheAccessTime(entry["lastAccessedAt"]) {
		entry["lastAccessedAt"] = now
		s.cacheAccessVersion++
	}
	copy, _ := cloneStateMap(entry)
	return copy
}

func (s *Server) tabCacheMetadata() []map[string]any {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	items := make([]map[string]any, 0, len(appObject(s.state.Settings["tab.cache"])))
	for _, raw := range appObject(s.state.Settings["tab.cache"]) {
		entry := appObject(raw)
		copy := make(map[string]any, len(entry))
		for key, value := range entry {
			if key != "payloadBase64" {
				copy[key], _ = cloneStateValue(value)
			}
		}
		items = append(items, copy)
	}
	return items
}

// Called with stateMu held after a successful disk write. Only recency may have
// changed since the private copy was taken; all durable updates hold storeMu.
func mergeCacheAccesses(next *diskState, current diskState) {
	currentItems := appObject(current.Settings["tab.cache"])
	for key, raw := range appObject(next.Settings["tab.cache"]) {
		entry, ok := raw.(map[string]any)
		if !ok || entry == nil {
			continue
		}
		access := appObject(currentItems[key])["lastAccessedAt"]
		if cacheAccessTime(access) > cacheAccessTime(entry["lastAccessedAt"]) {
			entry["lastAccessedAt"] = access
		}
	}
}

func (s *Server) flushCacheAccesses() error {
	s.stateMu.RLock()
	dirty := s.cacheAccessVersion != s.cacheAccessPersisted
	s.stateMu.RUnlock()
	if !dirty || s.aead == nil {
		return nil
	}
	return s.update(func(*diskState) error { return nil })
}
