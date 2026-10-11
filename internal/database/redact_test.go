package database

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactExternalServiceAndMQCredentials(t *testing.T) {
	secrets := []string{"http-password", "bearer token/a?b", "api-value", "oauth-secret", "signing-key", "access-key", "rocket-secret", "kafka-secret", "jaas-password", `escaped"password`}
	config := Config{Raw: map[string]any{"external_config": map[string]any{
		"auth":         map[string]any{"password": secrets[0], "token": secrets[1], "value": secrets[2], "clientSecret": secrets[3]},
		"tokenSigning": map[string]any{"key": secrets[4]},
		"extra":        map[string]any{"accessKey": secrets[5], "secret_key": secrets[6], "saslPassword": secrets[7], "properties": map[string]any{"sasl.jaas.config": `LoginModule required username="admin" password="jaas-password"; OtherModule password="escaped\"password";`}},
	}}}
	cause := errors.New("transport refused " + strings.Join(secrets, " | ") + " " + url.QueryEscape(secrets[1]))
	result := redact(cause, config)
	for _, secret := range secrets {
		if strings.Contains(result.Error(), secret) || strings.Contains(result.Error(), url.QueryEscape(secret)) {
			t.Errorf("credential exposed: %q in %s", secret, result.Error())
		}
	}
	if !errors.Is(result, cause) {
		t.Error("error cause lost")
	}
	if !strings.Contains(result.Error(), "transport refused") {
		t.Error("useful error context lost")
	}
}
