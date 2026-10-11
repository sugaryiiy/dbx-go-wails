package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

type recoveryContract struct {
	Version     int     `json:"contractVersion"`
	Category    string  `json:"category"`
	Retryable   *bool   `json:"retryable"`
	Disposition string  `json:"sessionDisposition"`
	Stage       string  `json:"stage"`
	Outcome     string  `json:"operationOutcome"`
	SessionID   *string `json:"agentSessionId"`
	SQLState    *string `json:"sqlState"`
	VendorCode  *int32  `json:"vendorCode"`
	Exception   *string `json:"exceptionClass"`
}

func asciiDiagnostic(value *string, limit int) bool {
	if value == nil {
		return true
	}
	if *value == "" || len(*value) > limit {
		return false
	}
	for _, r := range *value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func validateRecovery(data json.RawMessage, sessionID string) error {
	invalid := errors.New("agent structured error contract violation; reconnect required")
	var contract recoveryContract
	if json.Unmarshal(data, &contract) != nil {
		return invalid
	}
	if contract.Version != 1 || contract.Retryable == nil {
		return invalid
	}
	if !strings.Contains("|timeout|canceled|connection|protocol|resource|sql|", "|"+contract.Category+"|") {
		return invalid
	}
	if !strings.Contains("|keep|quarantine|replace_runtime|", "|"+contract.Disposition+"|") {
		return invalid
	}
	if !strings.Contains("|request|checkout|connect|validate|execute|fetch|cancel|close|", "|"+contract.Stage+"|") {
		return invalid
	}
	if contract.Outcome != "not_started" && contract.Outcome != "unknown" {
		return invalid
	}
	if sessionID != "" && (contract.SessionID == nil || *contract.SessionID != sessionID) {
		return invalid
	}
	if !asciiDiagnostic(contract.SessionID, 200) || !asciiDiagnostic(contract.SQLState, 16) || !asciiDiagnostic(contract.Exception, 160) {
		return invalid
	}
	switch contract.Category {
	case "timeout", "canceled":
		if contract.Disposition != "quarantine" {
			return invalid
		}
	case "connection":
		if contract.Disposition == "replace_runtime" {
			return invalid
		}
	case "sql":
		if contract.Disposition == "replace_runtime" || !strings.Contains("|execute|fetch|cancel|close|", "|"+contract.Stage+"|") {
			return invalid
		}
	case "resource":
		if contract.Disposition != "replace_runtime" && contract.Outcome != "not_started" {
			return invalid
		}
	case "protocol":
		if contract.Disposition == "replace_runtime" && contract.Outcome != "unknown" {
			return invalid
		}
	}
	return nil
}
