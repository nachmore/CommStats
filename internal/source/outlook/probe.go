// Package outlook is the Microsoft Outlook (web) comm-source. Because this
// tenant blocks both OAuth device-code (Conditional Access) and admin-consent
// for first-party Graph clients, we instead reuse the already-consented Outlook
// web client's session: a CDP-driven browser signs in once (persistent
// profile), and we sniff the bearer token OWA uses. That token's audience is
// https://outlook.office.com, so we call the outlook.office.com REST API
// (/api/v2.0/me/...) rather than the Graph API.
package outlook

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// decodeJWTClaims pulls the aud and scp claims out of a JWT without verifying
// it (we only need to know what the token is for). Returns ("", "") on failure.
func decodeJWTClaims(tok string) (aud, scp string) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Aud string `json:"aud"`
		Scp string `json:"scp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ""
	}
	return claims.Aud, claims.Scp
}
