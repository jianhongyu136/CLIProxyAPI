package helps

import (
	"testing"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexInstallationIDGeneratedPerAuthWhenMissing(t *testing.T) {
	clientBody := []byte(`{"model":"gpt-5-codex"}`)
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true}`)
	firstBody := EnsureCodexInstallationID(rawJSON, clientBody, &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"})
	secondBody := EnsureCodexInstallationID(rawJSON, clientBody, &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"})
	otherBody := EnsureCodexInstallationID(rawJSON, clientBody, &cliproxyauth.Auth{ID: "auth-2", Provider: "codex"})

	expectedFirstID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:installation:auth-1")).String()
	firstID := gjson.GetBytes(firstBody, "client_metadata.x-codex-installation-id").String()
	if firstID != expectedFirstID {
		t.Fatalf("generated installation id = %q, want %q; body=%s", firstID, expectedFirstID, string(firstBody))
	}
	if secondID := gjson.GetBytes(secondBody, "client_metadata.x-codex-installation-id").String(); secondID != firstID {
		t.Fatalf("generated installation id must be stable, second = %q, first = %q", secondID, firstID)
	}
	if otherID := gjson.GetBytes(otherBody, "client_metadata.x-codex-installation-id").String(); otherID == firstID || otherID == "" {
		t.Fatalf("generated installation id must differ per auth, other = %q, first = %q", otherID, firstID)
	}
	for _, payload := range [][]byte{clientBody, rawJSON} {
		if gotClientID := gjson.GetBytes(payload, "client_metadata.x-codex-installation-id").String(); gotClientID != "" {
			t.Fatalf("input payload was mutated with installation id %q", gotClientID)
		}
	}
}

func TestCodexInstallationIDPreservesExistingID(t *testing.T) {
	clientBody := []byte(`{"client_metadata":{"x-codex-installation-id":"client-install"}}`)
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{name: "restore client ID", body: []byte(`{"model":"gpt-5-codex"}`), want: "client-install"},
		{name: "preserve upstream ID", body: []byte(`{"client_metadata":{"x-codex-installation-id":"upstream-install"}}`), want: "upstream-install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := EnsureCodexInstallationID(tc.body, clientBody, auth)
			if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != tc.want {
				t.Fatalf("installation id = %q, want %q", got, tc.want)
			}
		})
	}
}
