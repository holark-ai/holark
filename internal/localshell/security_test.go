package localshell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/localconnection"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
)

func request(t *testing.T, h http.Handler, method, path, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:43123"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:43123"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLaunchSecretCookieAndSecurityBoundary(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h, secret, err := newHandler("127.0.0.1:43123", "cli-token", api)
	if err != nil {
		t.Fatal(err)
	}
	if got := request(t, h, "GET", "/api/test", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", got.Code)
	}
	var session string
	for attempt := 0; attempt < 3; attempt++ {
		// Each exchange starts without a cookie, like a fresh browser context.
		ex := request(t, h, "POST", "/api/launch-session", "http://127.0.0.1:43123", `{"secret":"`+secret+`"}`)
		if ex.Code != http.StatusNoContent {
			t.Fatalf("exchange %d=%d %s", attempt, ex.Code, ex.Body.String())
		}
		cookies := ex.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("exchange cookies=%v", cookies)
		}
		c := cookies[0]
		if c.Name != "holark_session_43123" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie=%+v", c)
		}
		if attempt > 0 && c.Value != session {
			t.Fatal("repeated exchange changed the session")
		}
		session = c.Value
		r := httptest.NewRequest("GET", "http://127.0.0.1:43123/api/test", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusTeapot {
			t.Fatalf("authenticated=%d", w.Code)
		}
	}
	if got := request(t, h, "POST", "/api/launch-session", "http://127.0.0.1:43123", `{"secret":"incorrect"}`); got.Code != http.StatusUnauthorized || len(got.Result().Cookies()) != 0 {
		t.Fatalf("incorrect token=%d cookies=%v", got.Code, got.Result().Cookies())
	}
	badHost := httptest.NewRequest("GET", "http://127.0.0.1:43123/", nil)
	badHost.Host = "evil.test"
	bw := httptest.NewRecorder()
	h.ServeHTTP(bw, badHost)
	if bw.Code != http.StatusMisdirectedRequest {
		t.Fatalf("host=%d", bw.Code)
	}
	if got := request(t, h, "GET", "/", "https://evil.test", ""); got.Code != http.StatusForbidden {
		t.Fatalf("origin=%d", got.Code)
	}
	if got := request(t, h, "POST", "/api/test", "", `{}`); got.Code != http.StatusForbidden {
		t.Fatalf("mutation=%d", got.Code)
	}
	front := request(t, h, "GET", "/", "", "")
	if front.Code != http.StatusOK || front.Header().Get("X-Frame-Options") != "DENY" || front.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("frontend=%d headers=%v", front.Code, front.Header())
	}
}

func TestBrowserSessionsIsolatedByPort(t *testing.T) {
	start := func(t *testing.T, address string) (*httptest.Server, string) {
		t.Helper()
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		h, secret, err := newHandler(listener.Addr().String(), "cli-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		if err != nil {
			listener.Close()
			t.Fatal(err)
		}
		server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: h}}
		server.Start()
		t.Cleanup(server.Close)
		return server, secret
	}
	exchange := func(t *testing.T, client *http.Client, server *httptest.Server, secret string, want int) *http.Cookie {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/launch-session", strings.NewReader(`{"secret":"`+secret+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", server.URL)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("exchange %s: status=%d, want=%d", server.URL, resp.StatusCode, want)
		}
		cookies := resp.Cookies()
		if want != http.StatusNoContent {
			if len(cookies) != 0 {
				t.Fatalf("failed exchange set cookies=%v", cookies)
			}
			return nil
		}
		if len(cookies) != 1 {
			t.Fatalf("exchange cookies=%v", cookies)
		}
		_, port, err := net.SplitHostPort(server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if cookies[0].Name != "holark_session_"+port {
			t.Fatalf("cookie name=%q, port=%s", cookies[0].Name, port)
		}
		return cookies[0]
	}
	check := func(t *testing.T, client *http.Client, server *httptest.Server, cookie *http.Cookie, want int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/test", nil)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("request %s: status=%d, want=%d", server.URL, resp.StatusCode, want)
		}
	}
	for _, order := range []struct {
		name          string
		first, second int
	}{{"A then B", 0, 1}, {"B then A", 1, 0}} {
		t.Run(order.name, func(t *testing.T) {
			a, launchA := start(t, "127.0.0.1:0")
			b, launchB := start(t, "127.0.0.1:0")
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
			servers := []*httptest.Server{a, b}
			secrets := []string{launchA, launchB}
			cookies := make([]*http.Cookie, 2)
			cookies[order.first] = exchange(t, client, servers[order.first], secrets[order.first], http.StatusNoContent)
			check(t, client, servers[order.first], nil, http.StatusNoContent)
			check(t, client, servers[order.second], nil, http.StatusUnauthorized)
			cookies[order.second] = exchange(t, client, servers[order.second], secrets[order.second], http.StatusNoContent)
			check(t, client, a, nil, http.StatusNoContent)
			check(t, client, b, nil, http.StatusNoContent)

			withoutJar := &http.Client{Timeout: 5 * time.Second}
			check(t, withoutJar, b, cookies[0], http.StatusUnauthorized)
			legacy := &http.Cookie{Name: "holark_session", Value: cookies[1].Value}
			check(t, withoutJar, b, legacy, http.StatusUnauthorized)

			address := a.Listener.Addr().String()
			a.Close()
			a, launchA = start(t, address)
			check(t, client, a, nil, http.StatusUnauthorized)
			check(t, client, b, nil, http.StatusNoContent)
			exchange(t, withoutJar, a, secrets[0], http.StatusUnauthorized)
			fresh := exchange(t, client, a, launchA, http.StatusNoContent)
			if fresh.Name != cookies[0].Name || fresh.Value == cookies[0].Value {
				t.Fatalf("restart must retain cookie name and rotate token")
			}
			check(t, client, a, nil, http.StatusNoContent)
			check(t, client, b, nil, http.StatusNoContent)
		})
	}
}

func TestCLIBearerAndWebSocketBoundary(t *testing.T) {
	h, _, err := newHandler("127.0.0.1:43123", "cli-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusSwitchingProtocols) }))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:43123/api/socket", nil)
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("websocket without auth=%d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1:43123/api/socket", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Authorization", "Bearer cli-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSwitchingProtocols {
		t.Fatalf("websocket with auth=%d", w.Code)
	}
}

func TestEmbeddedProductionFrontendAndSPAFallback(t *testing.T) {
	h, _, err := newHandler("127.0.0.1:43123", "cli-token", http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	index := request(t, h, http.MethodGet, "/holons/example", "", "")
	if index.Code != http.StatusOK {
		t.Fatalf("fallback=%d", index.Code)
	}
	body := index.Body.String()
	if !strings.Contains(body, `<div id="root"></div>`) || !strings.Contains(body, `src="/launch.js"`) {
		t.Fatalf("not production index: %s", body)
	}
	start := strings.Index(body, `src="/assets/`)
	if start < 0 {
		t.Fatalf("index has no generated asset: %s", body)
	}
	start += len(`src="`)
	end := strings.Index(body[start:], `"`)
	asset := request(t, h, http.MethodGet, body[start:start+end], "", "")
	if asset.Code != http.StatusOK || asset.Body.Len() == 0 {
		t.Fatalf("asset=%d bytes=%d", asset.Code, asset.Body.Len())
	}
	missing := request(t, h, http.MethodGet, "/assets/not-present.js", "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset=%d", missing.Code)
	}
	launch := request(t, h, http.MethodGet, "/launch.js", "", "")
	if launch.Code != http.StatusOK || !strings.Contains(launch.Body.String(), "/api/launch-session") || !strings.Contains(launch.Body.String(), "__HOLARK_LAUNCH_READY__") {
		t.Fatalf("launch bootstrap=%d %q", launch.Code, launch.Body.String())
	}
}

func TestRunBindsLoopbackAndPublishesPrivateConnectionWithoutAuth0(t *testing.T) {
	runtime := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var output strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ListenAddress: "127.0.0.1:0", RuntimeDirectory: runtime, HomeDirectory: t.TempDir(), Stdout: &output})
	}()
	repositoryID, err := gitadapter.RepositoryID(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	scopedPath, err := localconnection.ScopedPath(runtime, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		var err error
		data, err = os.ReadFile(scopedPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("connection file not published")
	}
	info, _ := os.Stat(runtime)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime mode=%o", info.Mode().Perm())
	}
	info, _ = os.Stat(scopedPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("connection mode=%o", info.Mode().Perm())
	}
	var c Connection
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.URL, "http://127.0.0.1:") || c.Token == "" {
		t.Fatalf("connection=%+v", c)
	}
	resp, err := http.Get(c.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("hosted auth route status=%d", resp.StatusCode)
	}
	client := &http.Client{}
	for _, contract := range []struct {
		path, contains string
	}{{"/api/v1/repository", `"default_branch"`}, {"/api/v1/holons", `"holons"`}, {"/holons/example", `<div id="root"></div>`}} {
		req, requestErr := http.NewRequest(http.MethodGet, c.URL+contract.path, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		response, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(contract.contains)) {
			t.Fatalf("local contract %s: status=%d body=%s err=%v", contract.path, response.StatusCode, body, readErr)
		}
	}
	cancel()
	<-done
	// Run owns the output writer until it returns; inspect the complete launch
	// output only after joining it so this assertion cannot race the write.
	if strings.Contains(output.String(), "auth0") || strings.Contains(output.String(), "authorize") {
		t.Fatalf("Auth0 reachable in launch URL: %s", output.String())
	}
	if _, err = os.Stat(scopedPath); !os.IsNotExist(err) {
		t.Fatalf("scoped connection remains: %v", err)
	}
}

func TestRunRejectsNonLoopback(t *testing.T) {
	if err := Run(t.Context(), Options{ListenAddress: "0.0.0.0:0"}); err == nil {
		t.Fatal("accepted non-loopback listener")
	}
}

func TestContentSecurityPolicySupportsRetainedFrontend(t *testing.T) {
	h, _, err := newHandler("127.0.0.1:43123", "cli-token", http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	policy := request(t, h, http.MethodGet, "/", "", "").Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP blocks retained frontend runtime styles: %q", policy)
	}
	if !strings.Contains(policy, "img-src 'self' https://avatars.githubusercontent.com") {
		t.Fatalf("CSP blocks GitHub member avatars: %q", policy)
	}
	if strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP permits inline scripts: %q", policy)
	}
}
