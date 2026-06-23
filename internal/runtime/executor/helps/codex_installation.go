package helps

import (
	"strings"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// EnsureCodexInstallationID preserves client installation IDs or generates a stable ID per auth.
func EnsureCodexInstallationID(rawJSON, userPayload []byte, auth *cliproxyauth.Auth) []byte {
	if auth == nil || strings.TrimSpace(auth.ID) == "" || len(rawJSON) == 0 {
		return rawJSON
	}
	installationID := strings.TrimSpace(gjson.GetBytes(userPayload, "client_metadata.x-codex-installation-id").String())
	if installationID == "" {
		installationID = strings.TrimSpace(gjson.GetBytes(rawJSON, "client_metadata.x-codex-installation-id").String())
	}
	if installationID == "" {
		name := strings.Join([]string{"cli-proxy-api", "codex", "installation", strings.TrimSpace(auth.ID)}, ":")
		installationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-installation-id", installationID)
	} else if !gjson.GetBytes(rawJSON, "client_metadata.x-codex-installation-id").Exists() {
		rawJSON, _ = sjson.SetBytes(rawJSON, "client_metadata.x-codex-installation-id", installationID)
	}
	return rawJSON
}
