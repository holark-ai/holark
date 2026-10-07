package attachments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/holark-ai/holark/internal/attachments"
	"github.com/holark-ai/holark/internal/attachments/httpapi"
	githubattachments "github.com/holark-ai/holark/internal/codehost/github/attachments"
)

func TestResolveGitHubImage(t *testing.T) {
	const source = "https://github.com/user-attachments/assets/12345678-1234-1234-1234-123456789abc"
	const legacySource = "https://github.com/owner/repo.name/assets/668535/5c3a4439-40e3-4868-8bf5-1069efef670e"
	const displayURL = "https://private-user-images.githubusercontent.com/123/image.png?jwt=signed&expires=123"
	for _, test := range []struct {
		name           string
		source         string
		directStatus   int
		markdownStatus int
		markup         string
		wantURL        string
	}{
		{name: "direct redirect", directStatus: http.StatusFound, wantURL: displayURL},
		{name: "direct public image", directStatus: http.StatusOK, wantURL: source},
		{name: "fine-grained token fallback", directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p><a href="` + source + `"><img src="` + strings.ReplaceAll(displayURL, "&", "&amp;") + `" alt="Image"></a></p>`, wantURL: displayURL},
		{name: "forbidden attachment fallback", directStatus: http.StatusForbidden, markdownStatus: http.StatusOK,
			markup: `<p><img src="` + displayURL + `"></p>`, wantURL: displayURL},
		{name: "unchanged private URL", directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p><img src="` + source + `"></p>`},
		{name: "untrusted display host", directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p><img src="https://example.com/image.png?jwt=signed"></p>`},
		{name: "missing image", directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p>Image unavailable</p>`},
		{name: "markdown access denied", directStatus: http.StatusNotFound, markdownStatus: http.StatusForbidden,
			markup: displayURL},
		{name: "legacy direct redirect", source: legacySource, directStatus: http.StatusFound, wantURL: displayURL},
		{name: "legacy public image", source: legacySource, directStatus: http.StatusOK, wantURL: legacySource},
		{name: "legacy Markdown fallback", source: legacySource, directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p><img src="` + strings.ReplaceAll(displayURL, "&", "&amp;") + `"></p>`, wantURL: displayURL},
		{name: "unchanged legacy private URL", source: legacySource, directStatus: http.StatusNotFound, markdownStatus: http.StatusOK,
			markup: `<p><img src="` + legacySource + `"></p>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestSource := test.source
			if requestSource == "" {
				requestSource = source
			}
			var markdownRequests atomic.Int32
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "token github_pat_test" {
					t.Error("GitHub request did not use the authenticated token scheme")
				}
				switch {
				case r.Host == "github.com" && r.Method == http.MethodGet && r.URL.Path == strings.TrimPrefix(requestSource, "https://github.com"):
					if test.directStatus == http.StatusFound {
						w.Header().Set("Location", displayURL)
					} else if test.directStatus == http.StatusOK {
						w.Header().Set("Content-Type", "image/png")
					}
					w.WriteHeader(test.directStatus)
				case r.Host == "api.github.com" && r.Method == http.MethodPost && r.URL.Path == "/markdown":
					markdownRequests.Add(1)
					if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/html" {
						t.Error("Markdown request did not use JSON input and HTML output")
					}
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode Markdown request: %v", err)
					}
					if payload["text"] != "![Image]("+requestSource+")" || payload["mode"] != "gfm" || payload["context"] != "owner/repo" {
						t.Errorf("Markdown request lost the permanent URL or repository context: %v", payload)
					}
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(test.markdownStatus)
					_, _ = w.Write([]byte(test.markup))
				default:
					t.Errorf("unexpected remote request (possibly followed a signed redirect): %s %s%s", r.Method, r.Host, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer github.Close()
			target, err := url.Parse(github.URL)
			if err != nil {
				t.Fatal(err)
			}
			provider := githubattachments.New(attachmentCredential{}, localGitHubTransport{target: target, base: github.Client().Transport})
			service := attachments.New("git@github.com:owner/repo.git", provider)
			mux := http.NewServeMux()
			httpapi.RegisterRoutes(func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }, service)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/attachments/resolve", strings.NewReader(`{"url":"`+requestSource+`"}`))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Error("temporary display URL response must not be cached")
			}
			if test.wantURL == "" {
				if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "jwt=") {
					t.Errorf("unsafe or unexpected error response: %d %s", response.Code, response.Body.String())
				}
			} else {
				var asset attachments.Asset
				if err := json.Unmarshal(response.Body.Bytes(), &asset); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusOK || asset.URL != test.wantURL {
					t.Errorf("resolve response = %d %s, want %s", response.Code, response.Body.String(), test.wantURL)
				}
			}
			wantMarkdownRequests := int32(0)
			if test.markdownStatus != 0 {
				wantMarkdownRequests = 1
			}
			if got := markdownRequests.Load(); got != wantMarkdownRequests {
				t.Errorf("Markdown requests = %d, want %d", got, wantMarkdownRequests)
			}
		})
	}
}

type attachmentCredential struct{}

func (attachmentCredential) Token(context.Context) (string, error) { return "github_pat_test", nil }
func (attachmentCredential) Request(context.Context, string, string, any, any) error {
	return errors.New("unexpected typed GitHub API request")
}

type localGitHubTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport localGitHubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	local := request.Clone(request.Context())
	local.Host = request.URL.Host
	local.URL.Scheme = transport.target.Scheme
	local.URL.Host = transport.target.Host
	return transport.base.RoundTrip(local)
}
