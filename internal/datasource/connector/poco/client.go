package poco

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
)

type client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newClient(ctx context.Context, cfg *config) (*client, error) {
	c := &client{baseURL: cfg.BaseURL, httpClient: datasource.NewConnectorHTTPClient(30 * time.Second)}
	body, err := json.Marshal(map[string]string{
		"grant_type": "password", "username": cfg.Username, "password": cfg.Password,
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.request(ctx, http.MethodPost, "/openapi/v1/auth/token", body, &result); err != nil {
		return nil, fmt.Errorf("poco authentication: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("%w: Poco did not return a login token", datasource.ErrInvalidCredentials)
	}
	c.token = result.AccessToken
	return c, nil
}

func (c *client) request(ctx context.Context, method, path string, body []byte, result interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("poco request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: Poco returned HTTP %d", datasource.ErrInvalidCredentials, resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("%w: Poco endpoint %s", datasource.ErrResourceNotFound, path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("poco endpoint %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode Poco response %s: %w", path, err)
	}
	return nil
}

func (c *client) listPage(ctx context.Context, limit, offset int) (*noteList, error) {
	var result struct {
		Code    int       `json:"code"`
		Message string    `json:"message"`
		Data    *noteList `json:"data"`
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
	if err := c.request(ctx, http.MethodGet, "/api/v1/ai-notes?"+query.Encode(), nil, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 || result.Data == nil {
		return nil, fmt.Errorf("poco note list failed: %s", result.Message)
	}
	return result.Data, nil
}

func (c *client) listNotes(ctx context.Context) ([]note, error) {
	var notes []note
	offset := 0
	for {
		page, err := c.listPage(ctx, 100, offset)
		if err != nil {
			return nil, err
		}
		notes = append(notes, page.Items...)
		if !page.HasMore {
			return notes, nil
		}
		if page.NextOffset == nil || *page.NextOffset <= offset {
			return nil, fmt.Errorf("poco returned an invalid pagination offset")
		}
		offset = *page.NextOffset
	}
}

func (c *client) getNote(ctx context.Context, id string) (*note, error) {
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    *note  `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/v1/ai-notes/"+url.PathEscape(id), nil, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 || result.Data == nil {
		return nil, fmt.Errorf("poco note detail failed: %s", result.Message)
	}
	return result.Data, nil
}
