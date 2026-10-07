package httpapi

import (
	"context"
	"github.com/holark-ai/holark/internal/ide"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

type serviceStub struct{ target string }

func (serviceStub) List(context.Context, string) ([]ide.IDE, error)          { return nil, nil }
func (serviceStub) Open(context.Context, string) (ide.IDE, error)            { return ide.IDE{}, nil }
func (serviceStub) Close(context.Context, string, string) (ide.IDE, error)   { return ide.IDE{}, nil }
func (s serviceStub) Target(context.Context, string, string) (string, error) { return s.target, nil }
func TestDirectProxyStaysLoopbackStripsCookieRewritesWorkbenchAndUsesUpstreamFramingPolicy(t *testing.T) {
	var cookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		io.WriteString(w, `<!doctype html><meta id="vscode-workbench-web-configuration" data-settings="{}">`)
	}))
	defer upstream.Close()
	if !strings.HasPrefix(upstream.URL, "http://127.0.0.1:") {
		t.Skip("test listener is not IPv4 loopback")
	}
	handler := New(serviceStub{upstream.URL})
	secured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		handler.ServeHTTP(w, r)
	})
	request := httptest.NewRequest("GET", "/api/v1/holons/h/ides/i/proxy/?holark-theme=light", nil)
	request.AddCookie(&http.Cookie{Name: "holark_session", Value: "secret"})
	response := httptest.NewRecorder()
	secured.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if cookie != "" {
		t.Fatalf("cookie leaked: %q", cookie)
	}
	if !strings.Contains(response.Body.String(), "Default Light Modern") {
		t.Fatalf("workbench not rewritten: %s", response.Body.String())
	}
	if got := response.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != "frame-ancestors 'self'" {
		t.Fatalf("content security policies=%q", got)
	}
	if got := response.Header().Values("X-Frame-Options"); len(got) != 1 || got[0] != "SAMEORIGIN" {
		t.Fatalf("frame options=%q", got)
	}
}

func TestDirectProxyRelaysWebSocketOnLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		kind, payload, err := connection.Read(r.Context())
		if err == nil {
			_ = connection.Write(r.Context(), kind, payload)
		}
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(New(serviceStub{upstream.URL}))
	defer proxy.Close()
	endpoint := "ws" + strings.TrimPrefix(proxy.URL, "http") + "/api/v1/holons/h/ides/i/proxy/socket"
	connection, _, err := websocket.Dial(t.Context(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err = connection.Write(t.Context(), websocket.MessageText, []byte("holark")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "holark" {
		t.Fatalf("payload=%q", payload)
	}
}

func TestProxyRejectsNonLoopbackRuntimeTargets(t *testing.T) {
	handler := New(serviceStub{"https://example.com"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/holons/h/ides/i/proxy/", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d", response.Code)
	}
}
