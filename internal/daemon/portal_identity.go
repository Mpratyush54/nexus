package daemon

import (
	"net/http"
	"strings"
)

// HeaderPortalUserID is sent by the Nexus web portal on same-machine bridge
// calls. Privileged /local/* endpoints require it to match the Desktop account.
const HeaderPortalUserID = "X-Nexus-Portal-User"

// portalOrigin reports whether the request is a cross-origin call from the
// Nexus web app (vs the local status page, which has no Origin or same-origin).
func portalOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	return proxyCORSOrigins()[origin]
}

// requireMatchingPortalUser rejects browser-portal calls that do not prove
// they are the same Nexus user as this Desktop. Local status HTML (no Origin)
// stays usable for sign-in. Healthz/status stay readable so the portal can
// detect a mismatch and show a banner.
func (d *Daemon) requireMatchingPortalUser(w http.ResponseWriter, r *http.Request) bool {
	if d == nil {
		writeErr(w, http.StatusServiceUnavailable, "daemon not configured")
		return false
	}
	if !portalOrigin(r) {
		return true
	}
	desktopID := strings.TrimSpace(d.StatusSnapshot().UserID)
	if desktopID == "" {
		writeErr(w, http.StatusForbidden, "desktop not signed in — sign in Nexus Desktop with the same account as the portal")
		return false
	}
	portalID := strings.TrimSpace(r.Header.Get(HeaderPortalUserID))
	if portalID == "" {
		writeErr(w, http.StatusForbidden, "portal user required — refresh the app or re-link Desktop")
		return false
	}
	if !strings.EqualFold(portalID, desktopID) {
		writeErr(w, http.StatusForbidden, "account mismatch — Desktop and portal are different Nexus users")
		return false
	}
	return true
}

// withPortalIdentityGate wraps a handler so portal (CORS) callers must match
// the Desktop signed-in user.
func (d *Daemon) withPortalIdentityGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.requireMatchingPortalUser(w, r) {
			return
		}
		next(w, r)
	}
}
