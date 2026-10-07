// Package attachments handles GitHub's user attachment protocol independently
// of issue and comment publication. GitHub is the only image storage provider.
package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/attachments"
	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
)

type Client interface {
	Request(context.Context, string, string, any, any) error
	Token(context.Context) (string, error)
}

type Provider struct {
	client Client
	http   *http.Client
}

func New(client Client, transport http.RoundTripper) *Provider {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Provider{client: client, http: &http.Client{
		Transport: transport, Timeout: time.Minute,
		// Never forward the GitHub token to a redirect or download image bytes.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (p *Provider) Upload(ctx context.Context, repositoryURL string, image attachments.Image) (attachments.Asset, error) {
	repo, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return attachments.Asset{}, attachments.ErrUnsupportedRepository
	}
	var metadata struct {
		ID          int64 `json:"id"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	endpoint := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	if err := p.client.Request(ctx, "GET", endpoint, nil, &metadata); err != nil {
		return attachments.Asset{}, errors.New("GitHub repository access could not be checked. Check your gh authentication and repository access.")
	}
	if !metadata.Permissions.Push {
		return attachments.Asset{}, attachments.ErrPermissionDenied
	}
	if metadata.ID <= 0 {
		return attachments.Asset{}, errors.New("GitHub did not identify the repository for this upload.")
	}
	query := url.Values{"repository_id": {strconv.FormatInt(metadata.ID, 10)}, "name": {image.Name}, "content_type": {image.ContentType}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://uploads.github.com/user-attachments/assets?"+query.Encode(), bytes.NewReader(image.Data))
	if err != nil {
		return attachments.Asset{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.request(req)
	if err != nil {
		return attachments.Asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return attachments.Asset{}, responseError(resp, true)
	}
	var asset attachments.Asset
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&asset); err != nil {
		return asset, errors.New("GitHub uploaded the image but its URL could not be read. Choose the image again to retry.")
	}
	return asset, nil
}

func (p *Provider) Resolve(ctx context.Context, repositoryURL, source string) (string, error) {
	if !attachments.IsGitHubAssetURL(source) {
		return "", attachments.ErrInvalidURL
	}
	if displayURL, err := p.resolveDirect(ctx, source); err == nil {
		return displayURL, nil
	}
	// Fine-grained tokens can access a repository but cannot fetch its private
	// attachments directly. GitHub's Markdown renderer supplies a signed URL.
	return p.resolveMarkdown(ctx, repositoryURL, source)
}

func (p *Provider) resolveDirect(ctx context.Context, source string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.request(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		location, err := resp.Location()
		if err != nil || !attachments.IsGitHubDisplayURL(location.String()) {
			return "", errors.New("GitHub did not return a usable image display URL.")
		}
		// Return only the signed remote URL. The browser loads bytes from GitHub;
		// Holark does not proxy, store, or serve the image.
		return location.String(), nil
	}
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return source, nil
	}
	return "", responseError(resp, false)
}

func (p *Provider) resolveMarkdown(ctx context.Context, repositoryURL, source string) (string, error) {
	repo, err := githubapi.ParseRepositoryURL(repositoryURL)
	if err != nil {
		return "", attachments.ErrUnsupportedRepository
	}
	payload, err := json.Marshal(map[string]string{
		"text": "![Image](" + source + ")", "mode": "gfm", "context": repo.Owner + "/" + repo.Name,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/markdown", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/html")
	resp, err := p.request(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", responseError(resp, false)
	}
	// The renderer returns HTML, including void img elements and escaped URL
	// query parameters. Read only the image source, never return rendered HTML.
	decoder := xml.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if element, ok := token.(xml.StartElement); ok && element.Name.Local == "img" {
			for _, attribute := range element.Attr {
				if attribute.Name.Local == "src" && !attachments.IsGitHubAssetURL(attribute.Value) && attachments.IsGitHubDisplayURL(attribute.Value) {
					return attribute.Value, nil
				}
			}
		}
	}
	return "", errors.New("GitHub did not return a usable image display URL.")
}

func (p *Provider) request(req *http.Request) (*http.Response, error) {
	token, err := p.client.Token(req.Context())
	if err != nil || token == "" {
		return nil, attachments.ErrAuthentication
	}
	req.Header.Set("Authorization", "token "+token)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	req.Header.Set("User-Agent", "Holark")
	resp, err := p.http.Do(req)
	if err != nil {
		// Transport errors can contain signed URLs. Do not expose them in notices.
		return nil, errors.New("GitHub could not be reached. Try again when your connection is available.")
	}
	return resp, nil
}

func responseError(resp *http.Response, upload bool) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return attachments.ErrAuthentication
	case http.StatusForbidden, http.StatusNotFound:
		if upload {
			return attachments.ErrPermissionDenied
		}
		return errors.New("This GitHub image is unavailable or your account does not have access to it.")
	case http.StatusRequestEntityTooLarge:
		return attachments.ErrImageTooLarge
	case http.StatusUnprocessableEntity:
		return errors.New("GitHub rejected this image. Check its type, size, and your authentication permissions.")
	case http.StatusTooManyRequests:
		return errors.New("GitHub rate limited this request. Wait briefly before trying again.")
	default:
		return fmt.Errorf("GitHub could not complete the image request (HTTP %d).", resp.StatusCode)
	}
}
