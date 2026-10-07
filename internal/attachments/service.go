// Package attachments uploads images to the repository's code host. Image bytes
// are used only for the upload; there is no local attachment store.
package attachments

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const MaximumImageBytes = 10 * 1024 * 1024

var (
	ErrInvalidImage          = errors.New("Choose a PNG, JPEG, GIF, WebP, or SVG image.")
	ErrImageTooLarge         = errors.New("Images must be no larger than 10 MB.")
	ErrUnsupportedRepository = errors.New("Connect this repository to GitHub before uploading images.")
	ErrPermissionDenied      = errors.New("Uploading images requires write access to the GitHub repository.")
	ErrAuthentication        = errors.New("GitHub authentication is unavailable. Sign in with gh auth login.")
	ErrInvalidURL            = errors.New("The GitHub attachment URL is invalid.")
)

type Image struct {
	Name        string
	ContentType string
	Data        []byte
}

type Asset struct {
	URL string `json:"url"`
}

type Provider interface {
	Upload(context.Context, string, Image) (Asset, error)
	Resolve(context.Context, string, string) (string, error)
}

type Service struct {
	repositoryURL string
	provider      Provider
}

func New(repositoryURL string, provider Provider) *Service {
	return &Service{repositoryURL: repositoryURL, provider: provider}
}

func (s *Service) Upload(ctx context.Context, name string, data []byte) (Asset, error) {
	if len(data) > MaximumImageBytes {
		return Asset{}, ErrImageTooLarge
	}
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if name == "." || name == "/" || len(name) > 255 || strings.ContainsAny(name, "\x00\r\n") {
		return Asset{}, ErrInvalidImage
	}
	contentType := imageContentType(data)
	extensions := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml"}
	if contentType == "" || extensions[strings.ToLower(path.Ext(name))] != contentType {
		return Asset{}, ErrInvalidImage
	}
	if s.repositoryURL == "" || s.provider == nil {
		return Asset{}, ErrUnsupportedRepository
	}
	asset, err := s.provider.Upload(ctx, s.repositoryURL, Image{Name: name, ContentType: contentType, Data: data})
	if err == nil && !IsGitHubAssetURL(asset.URL) {
		return Asset{}, errors.New("GitHub returned an invalid attachment URL.")
	}
	return asset, err
}

func (s *Service) Resolve(ctx context.Context, source string) (Asset, error) {
	if !IsGitHubAssetURL(source) {
		return Asset{}, ErrInvalidURL
	}
	if s.provider == nil {
		return Asset{}, ErrUnsupportedRepository
	}
	resolved, err := s.provider.Resolve(ctx, s.repositoryURL, source)
	if err == nil && !IsGitHubDisplayURL(resolved) {
		return Asset{}, errors.New("GitHub returned an invalid image display URL.")
	}
	return Asset{URL: resolved}, err
}

var assetPath = regexp.MustCompile(`^/(user-attachments/assets|[a-zA-Z0-9-]+/[a-zA-Z0-9_.-]+/assets/[0-9]+)/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var assetStorageHost = regexp.MustCompile(`^github-production-(user-asset|repository-file)-[a-z0-9]+\.s3(\.[a-z0-9-]+)?\.amazonaws\.com$`)

func IsGitHubAssetURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Scheme, "https") && strings.EqualFold(u.Host, "github.com") && u.User == nil && u.RawQuery == "" && u.Fragment == "" && assetPath.MatchString(u.EscapedPath())
}

// Only GitHub's attachment hosts can receive a resolved image reference. Signed
// URLs are ephemeral display values and must never replace the saved Markdown.
func IsGitHubDisplayURL(raw string) bool {
	if IsGitHubAssetURL(raw) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.HasSuffix(host, ".githubusercontent.com") || assetStorageHost.MatchString(host)
}

func imageContentType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		if root, ok := token.(xml.StartElement); ok {
			if root.Name.Local != "svg" || (root.Name.Space != "" && root.Name.Space != "http://www.w3.org/2000/svg") {
				return ""
			}
			// Reject malformed XML before sending bytes to the provider.
			for {
				_, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					return "image/svg+xml"
				}
				if err != nil {
					return ""
				}
			}
		}
	}
}
