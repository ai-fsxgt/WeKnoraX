package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SystemParserEngineRepository persists platform-owned parser configurations.
type SystemParserEngineRepository interface {
	List(ctx context.Context) ([]types.SystemParserEngine, error)
	Get(ctx context.Context, engine string) (*types.SystemParserEngine, error)
	Upsert(ctx context.Context, engine *types.SystemParserEngine) error
}
