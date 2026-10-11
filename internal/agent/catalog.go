// Package agent implements DBX's standalone driver protocol without a Rust host.
package agent

import (
	_ "embed"
	"encoding/json"
)

// Catalog is copied from the shared database manifest, so driver profile aliases
// select the same package in both desktop implementations.
//
//go:embed catalog.json
var catalogJSON []byte

type Profile struct {
	Profile      string `json:"profile"`
	AgentKey     string `json:"agentKey"`
	PackageKey   string `json:"packageKey"`
	Label        string `json:"label"`
	StoreVisible bool   `json:"storeVisible"`
}
type Entry struct {
	DBType      string    `json:"dbType"`
	Label       string    `json:"label"`
	AgentKey    string    `json:"agentKey"`
	DefaultPort int       `json:"defaultPort"`
	Profiles    []Profile `json:"driverProfiles"`
}

var Catalog = func() []Entry {
	var entries []Entry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic(err)
	}
	return entries
}()

func Key(dbType, profile string) (string, bool) {
	// InfluxDB 1.x/2.x use the host's HTTP driver; no standalone package exists.
	if dbType == "influxdb" {
		return "", false
	}
	// SQL Server's original native Rust driver is replaced by its supported
	// standalone JDBC component when running the Go host.
	if dbType == "sqlserver" && (profile == "" || profile == "native") {
		return "sqlserver-legacy", true
	}
	for _, entry := range Catalog {
		if entry.DBType != dbType {
			continue
		}
		if profile != "" {
			for _, p := range entry.Profiles {
				if p.Profile == profile {
					if p.PackageKey != "" {
						return p.PackageKey, true
					}
					if p.AgentKey != "" {
						return p.AgentKey, true
					}
				}
			}
			if profile != "native" && profile != dbType {
				return "", false
			}
		}
		return entry.AgentKey, entry.AgentKey != ""
	}
	return "", false
}
func UsesDatabase(dbType string) bool {
	// These implementations already use Go's in-process native clients.
	if dbType == "mongodb" || dbType == "redis" || dbType == "clickhouse" {
		return false
	}
	if dbType == "mq" {
		return true
	}
	_, ok := Key(dbType, "")
	return ok
}
func DefaultPort(dbType string) int {
	for _, entry := range Catalog {
		if entry.DBType == dbType {
			return entry.DefaultPort
		}
	}
	return 0
}
func Labels() map[string]string {
	labels := map[string]string{}
	for _, entry := range Catalog {
		if entry.DBType == "influxdb" {
			continue
		}
		if entry.AgentKey != "" {
			labels[entry.AgentKey] = entry.Label
		}
		for _, p := range entry.Profiles {
			if p.StoreVisible && p.AgentKey != "" {
				key := p.AgentKey
				if p.PackageKey != "" {
					key = p.PackageKey
				}
				labels[key] = p.Label
			}
		}
	}
	return labels
}
