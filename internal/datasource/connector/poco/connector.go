package poco

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

var _ datasource.FullSyncStreamingConnector = (*Connector)(nil)

// Connector imports completed Poco notes as composed Markdown documents.
type Connector struct{}

// NewConnector creates a Poco connector.
func NewConnector() *Connector { return &Connector{} }

// Type returns the Poco connector identifier.
func (c *Connector) Type() string { return types.ConnectorTypePoco }

// Validate authenticates the user and verifies access to the note list.
func (c *Connector) Validate(ctx context.Context, ds *types.DataSourceConfig) error {
	cfg, err := parseConfig(ds)
	if err != nil {
		return err
	}
	cli, err := newClient(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = cli.listPage(ctx, 1, 0)
	return err
}

// ListResources validates access and returns the all-notes scope.
func (c *Connector) ListResources(
	ctx context.Context, ds *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	if parentID != "" {
		return []types.Resource{}, nil
	}
	if err := c.Validate(ctx, ds); err != nil {
		return nil, err
	}
	return []types.Resource{{ExternalID: allNotesID, Name: "Poco", Type: "poco_notes"}}, nil
}

// ResolveResourceAncestors is empty because the all-notes scope has no hierarchy.
func (c *Connector) ResolveResourceAncestors(
	context.Context, *types.DataSourceConfig, []string,
) ([]string, error) {
	return []string{}, nil
}

// FetchAll collects a full fetch for callers of the base connector interface.
func (c *Connector) FetchAll(
	ctx context.Context, ds *types.DataSourceConfig, resourceIDs []string,
) ([]types.FetchedItem, error) {
	if ds == nil {
		return nil, datasource.ErrInvalidConfig
	}
	config := *ds
	config.ResourceIDs = resourceIDs
	handler := &collectHandler{}
	_, err := c.FetchStream(ctx, &config, nil, handler)
	return handler.items, err
}

// FetchIncremental collects changed items using the same streaming fetch.
func (c *Connector) FetchIncremental(
	ctx context.Context, ds *types.DataSourceConfig, cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	handler := &collectHandler{}
	next, err := c.FetchStream(ctx, ds, cursor, handler)
	return handler.items, next, err
}

// FetchStream imports changed notes and checkpoints each accepted item.
func (c *Connector) FetchStream(
	ctx context.Context, ds *types.DataSourceConfig, cursor *types.SyncCursor, handler datasource.StreamHandler,
) (*types.SyncCursor, error) {
	return c.fetchStream(ctx, ds, cursor, handler, false)
}

// FetchFullStream refreshes all selected notes while keeping the deletion baseline.
func (c *Connector) FetchFullStream(
	ctx context.Context, ds *types.DataSourceConfig, cursor *types.SyncCursor, handler datasource.StreamHandler,
) (*types.SyncCursor, error) {
	return c.fetchStream(ctx, ds, cursor, handler, true)
}

func (c *Connector) fetchStream(
	ctx context.Context, ds *types.DataSourceConfig, cursor *types.SyncCursor,
	handler datasource.StreamHandler, forceFull bool,
) (*types.SyncCursor, error) {
	cfg, err := parseConfig(ds)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ds.ResourceIDs, allNotesID) {
		return nil, fmt.Errorf("%w: select all Poco notes to sync", datasource.ErrInvalidConfig)
	}
	sourceKey := fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.BaseURL+"\n"+cfg.Username)))
	var state syncCursor
	if cursor != nil {
		raw, err := json.Marshal(cursor.ConnectorCursor)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("decode Poco sync cursor: %w", err)
		}
		if state.Items == nil {
			return nil, fmt.Errorf("invalid Poco sync cursor: items are missing")
		}
	}
	if state.SourceKey != sourceKey {
		state = syncCursor{SourceKey: sourceKey, Items: make(map[string]string)}
	}
	baseline := maps.Clone(state.Items)
	if forceFull {
		state.PendingFullIDs = make(map[string]bool, len(state.Items))
		for id := range state.Items {
			state.PendingFullIDs[id] = true
		}
		if err := checkpoint(ctx, handler, state); err != nil {
			return nil, err
		}
	}
	cli, err := newClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	notes, err := cli.listNotes(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(notes))
	signaturePrefix := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{
		cfg.BaseURL, cfg.Username, strings.Join(cfg.ContentParts, ","),
	}, "\n"))))
	for _, item := range notes {
		seen[item.ID] = true
		if item.Status != "completed" {
			delete(state.PendingFullIDs, item.ID)
			continue
		}
		signature := signaturePrefix + ":" + item.UpdatedAt.Format(time.RFC3339Nano)
		if !state.PendingFullIDs[item.ID] && state.Items[item.ID] == signature {
			continue
		}
		detail, err := cli.getNote(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		if detail.Status != "completed" {
			continue
		}
		accepted, err := handler.Emit(ctx, fetchedNote(cfg, *detail))
		if err != nil {
			return nil, err
		}
		if accepted {
			state.Items[item.ID] = signaturePrefix + ":" + detail.UpdatedAt.Format(time.RFC3339Nano)
			delete(state.PendingFullIDs, item.ID)
		}
		if err := checkpoint(ctx, handler, state); err != nil {
			return nil, err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(baseline)) {
		if seen[id] {
			continue
		}
		_, err := cli.getNote(ctx, id)
		if err == nil {
			continue
		}
		if !errors.Is(err, datasource.ErrResourceNotFound) {
			return nil, err
		}
		accepted, err := handler.Emit(ctx, types.FetchedItem{ExternalID: id, IsDeleted: true})
		if err != nil {
			return nil, err
		}
		if accepted {
			delete(state.Items, id)
			delete(state.PendingFullIDs, id)
		}
		if err := checkpoint(ctx, handler, state); err != nil {
			return nil, err
		}
	}
	return encodedCursor(state)
}

func checkpoint(ctx context.Context, handler datasource.StreamHandler, state syncCursor) error {
	cursor, err := encodedCursor(state)
	if err != nil {
		return err
	}
	return handler.Checkpoint(ctx, cursor)
}

func encodedCursor(state syncCursor) (*types.SyncCursor, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return &types.SyncCursor{LastSyncTime: time.Now().UTC(), ConnectorCursor: fields}, nil
}

func noteTitle(item note) string {
	if item.Title != "" {
		return item.Title
	}
	return item.FileName
}

func fetchedNote(cfg *config, item note) types.FetchedItem {
	sections := []string{"# " + noteTitle(item)}
	for _, part := range cfg.ContentParts {
		var heading, content string
		switch part {
		case "transcript":
			heading, content = "全文", item.Transcript
		case "summary":
			heading, content = "摘要", item.Summary
		case "key_points":
			heading = "要点"
			for _, point := range item.KeyPoints {
				content += "- " + point + "\n"
			}
		case "todo_items":
			heading = "待办"
			for _, todo := range item.TodoItems {
				content += "- " + todo + "\n"
			}
		}
		sections = append(sections, "## "+heading+"\n\n"+strings.TrimSpace(content))
	}
	return types.FetchedItem{
		ExternalID: item.ID, Title: noteTitle(item), Content: []byte(strings.Join(sections, "\n\n") + "\n"),
		ContentType: "text/markdown", UpdatedAt: item.UpdatedAt,
		FileName:         strings.NewReplacer("/", "_", "\\", "_").Replace(noteTitle(item)) + ".md",
		SourceResourceID: allNotesID,
		Metadata:         map[string]string{"channel": types.ConnectorTypePoco},
	}
}

type collectHandler struct {
	items []types.FetchedItem
}

func (h *collectHandler) Emit(_ context.Context, item types.FetchedItem) (bool, error) {
	h.items = append(h.items, item)
	return true, nil
}

func (h *collectHandler) Checkpoint(context.Context, *types.SyncCursor) error { return nil }
