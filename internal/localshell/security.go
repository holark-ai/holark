package localshell

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// The local frontend uses runtime styles for xterm's ANSI palette
// and geometry, Monaco layout, and interactive panel positioning. Scripts stay
// restricted to same-origin assets; only styles receive the inline exception.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' https://avatars.githubusercontent.com https:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

//go:embed frontend/*
var frontendFiles embed.FS

type security struct {
	launch, session, cli string
	cookieName           string
}

func token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newHandler(host, cliToken string, api http.Handler) (http.Handler, string, error) {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return nil, "", err
	}
	launch, err := token()
	if err != nil {
		return nil, "", err
	}
	session, err := token()
	if err != nil {
		return nil, "", err
	}
	files, err := fs.Sub(frontendFiles, "frontend")
	if err != nil {
		return nil, "", err
	}
	// Cookies are shared across ports, so each listener needs its own name.
	s := &security{launch: launch, session: session, cli: cliToken, cookieName: "holark_session_" + port}
	if api == nil {
		api = http.NotFoundHandler()
	}
	route := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/launch-session":
			s.exchange(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			if !s.authenticated(r) {
				jsonError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			api.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/auth/"):
			http.NotFound(w, r)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			w.Header().Set("Cache-Control", "no-store")
			asset := "dist/index.html"
			if r.URL.Path == "/launch.js" {
				asset = "launch.js"
			} else if strings.HasPrefix(r.URL.Path, "/assets/") {
				asset = "dist" + r.URL.Path
				if _, err := fs.Stat(files, asset); err != nil {
					http.NotFound(w, r)
					return
				}
			}
			http.ServeFileFS(w, r, files, asset)
		default:
			jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
	origin := "http://" + host
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Host != host {
			jsonError(w, http.StatusMisdirectedRequest, "invalid host")
			return
		}
		o := strings.TrimSpace(r.Header.Get("Origin"))
		if o != "" && o != origin {
			jsonError(w, http.StatusForbidden, "invalid origin")
			return
		}
		if mutates(r.Method) && o != origin && !s.cliAuthenticated(r) {
			jsonError(w, http.StatusForbidden, "same-origin request required")
			return
		}
		route.ServeHTTP(w, r)
	})
	return h, launch, nil
}

func (s *security) exchange(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var body struct {
		Secret string `json:"secret"`
	}
	if d.Decode(&body) != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !equal(body.Secret, s.launch) {
		jsonError(w, http.StatusUnauthorized, "invalid or expired launch secret")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}
func (s *security) cliAuthenticated(r *http.Request) bool {
	scheme, value, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	return s.cli != "" && ok && strings.EqualFold(scheme, "Bearer") && equal(strings.TrimSpace(value), s.cli)
}
func (s *security) authenticated(r *http.Request) bool {
	if s.cliAuthenticated(r) {
		return true
	}
	c, err := r.Cookie(s.cookieName)
	return err == nil && equal(c.Value, s.session)
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func mutates(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
