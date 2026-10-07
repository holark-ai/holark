// Package httpapi exposes the IDE domain and its direct loopback proxy.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/holark-ai/holark/internal/ide"
	"github.com/holark-ai/holark/internal/ideworkbench"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type Service interface {
	List(context.Context, string) ([]ide.IDE, error)
	Open(context.Context, string) (ide.IDE, error)
	Close(context.Context, string, string) (ide.IDE, error)
	Target(context.Context, string, string) (string, error)
}
type Handler struct{ s Service }

func New(s Service) http.Handler {
	h := &Handler{s}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/holons/{id}/ides", h.list)
	m.HandleFunc("POST /api/v1/holons/{id}/ides", h.open)
	m.HandleFunc("POST /api/v1/holons/{id}/ides/{ideID}/close", h.close)
	m.HandleFunc("/api/v1/holons/{id}/ides/{ideID}/proxy", h.redirect)
	m.HandleFunc("/api/v1/holons/{id}/ides/{ideID}/proxy/", h.proxy)
	return m
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	v, e := h.s.List(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	if v == nil {
		v = []ide.IDE{}
	}
	write(w, 200, v)
}
func (h *Handler) open(w http.ResponseWriter, r *http.Request) {
	v, e := h.s.Open(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 201, v)
}
func (h *Handler) close(w http.ResponseWriter, r *http.Request) {
	v, e := h.s.Close(r.Context(), r.PathValue("id"), r.PathValue("ideID"))
	if e != nil {
		fail(w, e)
		return
	}
	write(w, 200, v)
}
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request) {
	if _, e := h.s.Target(r.Context(), r.PathValue("id"), r.PathValue("ideID")); e != nil {
		fail(w, e)
		return
	}
	http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
}
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request) {
	raw, e := h.s.Target(r.Context(), r.PathValue("id"), r.PathValue("ideID"))
	if e != nil {
		fail(w, e)
		return
	}
	target, e := url.Parse(raw)
	if e != nil {
		fail(w, e)
		return
	}
	if target.Scheme != "http" || target.Hostname() != "127.0.0.1" || target.Port() == "" {
		write(w, http.StatusBadGateway, map[string]string{"error": "IDE target is not a private loopback listener"})
		return
	}
	theme := r.URL.Query().Get("holark-theme")
	if theme != "light" {
		theme = "dark"
	}
	q := r.URL.Query()
	q.Del("holark-theme")
	r.URL.RawQuery = q.Encode()
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(req *http.Request) { original(req); req.Host = target.Host; req.Header.Del("Cookie") }
	proxy.ModifyResponse = func(response *http.Response) error {
		if r.Method != http.MethodGet || r.URL.Path != ide.BasePath(r.PathValue("id"), r.PathValue("ideID"))+"/" || !ideworkbench.IsHTMLContentType(response.Header.Get("Content-Type")) {
			return nil
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, ideworkbench.MaxHTMLBytes+1))
		response.Body.Close()
		if e != nil || len(body) > ideworkbench.MaxHTMLBytes {
			return errors.New("invalid VS Code workbench response")
		}
		body, e = ideworkbench.Rewrite(body, theme)
		if e != nil {
			return e
		}
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		response.ContentLength = int64(len(body))
		response.Header.Del("Content-Length")
		return nil
	}
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
		write(response, http.StatusBadGateway, map[string]string{"error": "IDE connection is unavailable"})
	}
	// The outer local shell denies framing by default. The IDE is intentionally
	// same-origin framed, so its upstream response must own the framing policy.
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("X-Frame-Options")
	proxy.ServeHTTP(w, r)
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	status := 500
	switch {
	case errors.Is(e, ide.ErrNotFound):
		status = 404
	case errors.Is(e, ide.ErrInvalid):
		status = 400
	case errors.Is(e, ide.ErrExists), errors.Is(e, ide.ErrNotReady):
		status = 409
	}
	write(w, status, map[string]string{"error": e.Error()})
}
