package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
)

func (wb *Web) setupMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()
		needsSetup := cfg.CheckSetup()
		if needsSetup != nil && r.URL.Path != "/settings" && r.URL.Path != "/api/config" {
			http.Redirect(w, r, fmt.Sprintf("/settings?inco=%s", needsSetup.Error()), http.StatusSeeOther)
			return
		}

		// strip inco from URL
		if inco := r.URL.Query().Get("inco"); inco != "" && needsSetup == nil && r.URL.Path == "/settings" {
			// redirect to the same URL without the inco parameter
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
		}
		next.ServeHTTP(w, r)
	})
}

func (wb *Web) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if setup is needed
		cfg := config.Get()
		if !cfg.UseAuth {
			next.ServeHTTP(w, r)
			return
		}

		isAPI := wb.isAPIRequest(r)

		if cfg.NeedsAuth() {
			if isAPI {
				wb.sendJSONError(w, "Authentication setup required", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/register", http.StatusSeeOther)
			}
			return
		}

		// Check for API token first
		if wb.isValidAPIToken(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Fall back to session authentication
		session, _ := wb.cookie.Get(r, "auth-session")
		auth, ok := session.Values["authenticated"].(bool)

		if !ok || !auth {
			if isAPI {
				wb.sendJSONError(w, "Authentication required. Please provide a valid API token in the Authorization header (Bearer <token>) or authenticate via session cookies.", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isAPIRequest checks if the request is for an API endpoint
func (wb *Web) isAPIRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}

// sendJSONError sends a JSON error response
func (wb *Web) sendJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	err := json.NewEncoder(w).Encode(map[string]interface{}{
		"error":  message,
		"status": statusCode,
	})
	if err != nil {
		return
	}
}
