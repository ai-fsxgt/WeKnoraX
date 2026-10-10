// Package poco synchronizes Poco AI notes using password authentication.
package poco

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

const allNotesID = "all_notes"

var contentPartOrder = []string{"transcript", "summary", "key_points", "todo_items"}

type config struct {
	BaseURL      string
	Username     string
	Password     string
	ContentParts []string
}

func parseConfig(ds *types.DataSourceConfig) (*config, error) {
	if ds == nil {
		return nil, datasource.ErrInvalidConfig
	}
	rawBaseURL, _ := ds.Settings["base_url"].(string)
	baseURL := strings.TrimRight(strings.TrimSpace(rawBaseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: settings.base_url must be an HTTP or HTTPS base URL", datasource.ErrInvalidConfig)
	}
	if err := datasource.ValidateConnectorBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("%w (for private Poco deployments, add the host to SSRF_WHITELIST)", err)
	}
	username, _ := ds.Credentials["username"].(string)
	password, _ := ds.Credentials["password"].(string)
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, fmt.Errorf("%w: Poco username and password are required", datasource.ErrInvalidCredentials)
	}
	data, err := parseContentParts(ds.Settings["content_parts"])
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(data))
	for _, part := range contentPartOrder {
		if slices.Contains(data, part) {
			parts = append(parts, part)
		}
	}
	return &config{
		BaseURL: baseURL, Username: username, Password: password, ContentParts: parts,
	}, nil
}

func parseContentParts(raw interface{}) ([]string, error) {
	values, ok := raw.([]interface{})
	if !ok || len(values) == 0 {
		return nil, fmt.Errorf("%w: select at least one Poco content part", datasource.ErrInvalidConfig)
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		part, ok := value.(string)
		if !ok || !slices.Contains(contentPartOrder, part) {
			return nil, fmt.Errorf("%w: unsupported Poco content part", datasource.ErrInvalidConfig)
		}
		parts = append(parts, part)
	}
	return parts, nil
}

type note struct {
	ID         string    `json:"note_id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	FileName   string    `json:"file_name"`
	Transcript string    `json:"transcript_text"`
	Summary    string    `json:"summary"`
	KeyPoints  []string  `json:"key_points"`
	TodoItems  []string  `json:"todo_items"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type noteList struct {
	Items      []note `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextOffset *int   `json:"next_offset"`
}

type syncCursor struct {
	Items          map[string]string `json:"items"`
	PendingFullIDs map[string]bool   `json:"pending_full_ids,omitempty"`
}
