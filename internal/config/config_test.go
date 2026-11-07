package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactedConfigHidesSecrets(t *testing.T) {
	c := config{}
	c.APIKeys.RiotAPIKey = "RGAPI-secret"
	c.Server.JWTSecret = "jwt-secret"
	c.Discord.Token = "discord-token"
	c.Database.Password = "db-password"
	c.Database.DatabaseURL = "postgresql://user:pw@host/db"
	c.Server.Port = "3001"

	raw, err := json.Marshal(c.redacted())
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, secret := range []string{"RGAPI-secret", "jwt-secret", "discord-token", "db-password", "user:pw@host"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into log output: %s", secret, out)
		}
	}
	if !strings.Contains(out, `"port":"3001"`) {
		t.Errorf("non-secret value missing from output: %s", out)
	}
	if strings.Count(out, redactedValue) != 5 {
		t.Errorf("expected 5 redacted fields, got: %s", out)
	}
}
