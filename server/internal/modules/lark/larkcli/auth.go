package larkcli

import (
	"strings"
	"time"
)

type authStatusWire struct {
	AppID              string   `json:"appId"`
	AppIDSnake         string   `json:"app_id"`
	Brand              string   `json:"brand"`
	Identity           string   `json:"identity"`
	MissingScopes      []string `json:"missingScopes"`
	MissingScopesSnake []string `json:"missing_scopes"`
	Identities         struct {
		User authIdentityWire `json:"user"`
	} `json:"identities"`
	// A few early CLI builds returned the user fields at the top level. Keep the
	// fallback so recorded contracts remain useful across the pinned range.
	User authIdentityWire `json:"user"`
}

type authIdentityWire struct {
	Status              string   `json:"status"`
	Available           *bool    `json:"available"`
	Message             string   `json:"message"`
	OpenID              string   `json:"openId"`
	OpenIDSnake         string   `json:"open_id"`
	UserName            string   `json:"userName"`
	UserNameSnake       string   `json:"user_name"`
	TokenStatus         string   `json:"tokenStatus"`
	TokenStatusSnake    string   `json:"token_status"`
	Scope               string   `json:"scope"`
	Scopes              []string `json:"scopes"`
	MissingScopes       []string `json:"missingScopes"`
	MissingScopesSnake  []string `json:"missing_scopes"`
	ExpiresAt           string   `json:"expiresAt"`
	ExpiresAtSnake      string   `json:"expires_at"`
	RefreshExpiresAt    string   `json:"refreshExpiresAt"`
	RefreshExpiresSnake string   `json:"refresh_expires_at"`
}

func (w authStatusWire) result(now time.Time) AuthStatus {
	user := w.Identities.User
	if user.Status == "" && user.TokenStatus == "" && user.OpenID == "" && user.UserName == "" {
		user = w.User
	}
	status := strings.ToLower(strings.TrimSpace(user.Status))
	status = strings.NewReplacer("-", "_", " ", "_").Replace(status)
	tokenStatus := user.TokenStatus
	if tokenStatus == "" {
		tokenStatus = user.TokenStatusSnake
	}
	tokenStatusLower := strings.ToLower(strings.TrimSpace(tokenStatus))
	expires := parseTime(firstNonEmpty(user.ExpiresAt, user.ExpiresAtSnake))
	refreshExpires := parseTime(firstNonEmpty(user.RefreshExpiresAt, user.RefreshExpiresSnake))
	openID := firstNonEmpty(user.OpenID, user.OpenIDSnake)
	name := firstNonEmpty(user.UserName, user.UserNameSnake)
	missing := append(append([]string(nil), w.MissingScopes...), w.MissingScopesSnake...)
	missing = append(missing, user.MissingScopes...)
	missing = append(missing, user.MissingScopesSnake...)
	granted := append([]string(nil), user.Scopes...)
	if len(granted) == 0 {
		granted = splitScopes(user.Scope)
	}
	// expiresAt belongs to the short-lived access token. lark-cli refreshes it
	// automatically on the next user API call while refreshExpiresAt remains in
	// the future, so it must not make the connector look signed out.
	expired := explicitExpiryStatus(tokenStatusLower) || explicitExpiryStatus(status) ||
		(!refreshExpires.IsZero() && !refreshExpires.After(now))
	available := user.Available == nil || *user.Available
	ready := status == "ready" || status == "ok" || status == "authenticated" ||
		status == "needs_refresh" || status == "refresh_needed"
	if status == "" {
		ready = strings.Contains(tokenStatusLower, "valid") || strings.Contains(tokenStatusLower, "ready") ||
			strings.Contains(strings.ToLower(user.Message), "refresh")
	}
	loggedIn := available && ready && !expired
	return AuthStatus{AppID: firstNonEmpty(w.AppID, w.AppIDSnake), Brand: w.Brand, LoggedIn: loggedIn, Expired: expired, UserName: name, OpenID: openID,
		TokenStatus: tokenStatus, ExpiresAt: expires, RefreshExpiresAt: refreshExpires,
		GrantedScopes: uniqueStrings(granted), MissingScopes: uniqueStrings(missing)}
}

func explicitExpiryStatus(value string) bool {
	return strings.Contains(value, "expired") || strings.Contains(value, "invalid") || strings.Contains(value, "revoked")
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func splitScopes(s string) []string { return strings.Fields(s) }
