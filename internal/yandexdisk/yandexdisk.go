// Package yandexdisk creates a fresh, publicly-shared blank document on a
// Yandex Disk account via Yandex's official REST API
// (https://yandex.ru/dev/disk-api/), so the exit-panel's "Add client" flow
// can generate a --url for the yandex/vyandex transports instead of an
// operator making a document by hand and copying the share link.
//
// This is unrelated to how transport/yandex actually talks to a document
// (an unauthenticated scrape of the public editor page): this package only
// needs an OAuth token once, to create+publish the doc; anyone can then use
// its public link exactly as if it had been made through the Yandex Docs UI.
package yandexdisk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIBase = "https://cloud-api.yandex.net/v1/disk"

// folder is where generated documents are kept, so they're easy for an
// operator to find (and clean up) in their own Disk if they ever want to.
const folder = "/openflux"

// Client creates documents on one Yandex account's Disk.
type Client struct {
	token string
	base  string
	http  *http.Client
}

// New builds a Client. token is a Yandex OAuth token with disk.write scope
// (https://oauth.yandex.ru/, "Яндекс.Диск REST API" application).
func New(token string) *Client {
	return NewWithBase(token, defaultAPIBase)
}

// NewWithBase is New with a non-default API base URL. Exported for callers
// in other packages that need to point a Client at a fake server in tests;
// production code should use New.
func NewWithBase(token, base string) *Client {
	return &Client{
		token: token,
		base:  base,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// CreateDoc uploads a blank .docx named after name to the account's Disk,
// publishes it, and returns its public share URL -- the same
// disk.yandex.ru/i/... format transport/yandex expects as --url.
func (c *Client) CreateDoc(ctx context.Context, name string) (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("yandex disk: no OAuth token configured")
	}

	if err := c.ensureFolder(ctx); err != nil {
		return "", fmt.Errorf("create %s folder: %w", folder, err)
	}

	path := fmt.Sprintf("%s/%s-%s.docx", folder, sanitizeName(name), randomHex(4))

	href, err := c.uploadHref(ctx, path)
	if err != nil {
		return "", fmt.Errorf("get upload link: %w", err)
	}
	if err := c.putFile(ctx, href, blankDocx()); err != nil {
		return "", fmt.Errorf("upload document: %w", err)
	}
	if err := c.publish(ctx, path); err != nil {
		return "", fmt.Errorf("publish document: %w", err)
	}
	publicURL, err := c.publicURL(ctx, path)
	if err != nil {
		return "", fmt.Errorf("read public url: %w", err)
	}
	return publicURL, nil
}

// ---- Disk REST API calls ----

// apiError is the shape of a Yandex Disk API error response.
type apiError struct {
	Message     string `json:"message"`
	Description string `json:"description"`
	ErrorName   string `json:"error"`
}

func (c *Client) do(ctx context.Context, method, rawURL string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// call does a JSON API request with no request body and decodes a JSON
// response into out (if non-nil). Any status outside 2xx (except the ones
// each caller special-cases) is turned into a readable error from the
// response body.
func (c *Client) call(ctx context.Context, method, rawURL string, out any) error {
	return c.callWithBody(ctx, method, rawURL, nil, out)
}

// callWithBody is call, but marshals body as the JSON request body.
func (c *Client) callWithBody(ctx context.Context, method, rawURL string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	resp, err := c.do(ctx, method, rawURL, reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, out)
}

func decode(resp *http.Response, out any) error {
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiError
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return fmt.Errorf("yandex disk: %s (%s)", apiErr.Message, apiErr.ErrorName)
		}
		return fmt.Errorf("yandex disk: unexpected status %d: %s", resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// ensureFolder creates the /openflux folder; already-exists (409) is success.
func (c *Client) ensureFolder(ctx context.Context) error {
	u := fmt.Sprintf("%s/resources?path=%s", c.base, url.QueryEscape(folder))
	resp, err := c.do(ctx, http.MethodPut, u, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	return decode(resp, nil)
}

func (c *Client) uploadHref(ctx context.Context, path string) (string, error) {
	u := fmt.Sprintf("%s/resources/upload?path=%s&overwrite=false", c.base, url.QueryEscape(path))
	var out struct {
		Href string `json:"href"`
	}
	if err := c.call(ctx, http.MethodGet, u, &out); err != nil {
		return "", err
	}
	if out.Href == "" {
		return "", fmt.Errorf("response had no upload href")
	}
	return out.Href, nil
}

func (c *Client) putFile(ctx context.Context, href string, data []byte) error {
	resp, err := c.do(ctx, http.MethodPut, href, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp, nil)
}

// publicSettingsRequest asks Yandex Disk to publish a resource with the
// public link open to editing, not just viewing (the default). Without
// this, transport/yandex's collaborative-editing scrape never gets a
// token/document.key -- the anonymous visitor only has read access, so
// there's no write session to hand out.
type publicSettingsRequest struct {
	PublicSettings struct {
		Accesses []publicAccess `json:"accesses"`
	} `json:"public_settings"`
}

type publicAccess struct {
	Macros []string `json:"macros"`
	Rights []string `json:"rights"`
}

func (c *Client) publish(ctx context.Context, path string) error {
	u := fmt.Sprintf("%s/resources/publish?path=%s&allow_address_access=true", c.base, url.QueryEscape(path))
	var body publicSettingsRequest
	body.PublicSettings.Accesses = []publicAccess{{Macros: []string{"all"}, Rights: []string{"write"}}}
	return c.callWithBody(ctx, http.MethodPut, u, body, nil)
}

func (c *Client) publicURL(ctx context.Context, path string) (string, error) {
	u := fmt.Sprintf("%s/resources?path=%s", c.base, url.QueryEscape(path))
	var out struct {
		PublicURL string `json:"public_url"`
	}
	if err := c.call(ctx, http.MethodGet, u, &out); err != nil {
		return "", err
	}
	if out.PublicURL == "" {
		return "", fmt.Errorf("document has no public_url yet")
	}
	return out.PublicURL, nil
}

// ---- helpers ----

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "client"
	}
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		case r == ' ':
			sb.WriteByte('-')
		}
	}
	if sb.Len() == 0 {
		return "client"
	}
	s := sb.String()
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is effectively unrecoverable; a fixed
		// fallback just risks a path collision, which the caller's own
		// overwrite=false already turns into a clear error either way.
		return "0000"
	}
	return hex.EncodeToString(buf)
}
