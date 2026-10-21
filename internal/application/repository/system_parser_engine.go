package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type systemParserEngineRepository struct{ db *gorm.DB }

// NewSystemParserEngineRepository creates the platform parser configuration repository.
func NewSystemParserEngineRepository(db *gorm.DB) interfaces.SystemParserEngineRepository {
	return &systemParserEngineRepository{db: db}
}

func (r *systemParserEngineRepository) List(ctx context.Context) ([]types.SystemParserEngine, error) {
	var engines []types.SystemParserEngine
	err := r.db.WithContext(ctx).Find(&engines).Error
	return engines, err
}

func (r *systemParserEngineRepository) Get(ctx context.Context, engine string) (*types.SystemParserEngine, error) {
	var record types.SystemParserEngine
	err := r.db.WithContext(ctx).Where("engine = ?", engine).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &record, err
}

func (r *systemParserEngineRepository) Upsert(ctx context.Context, engine *types.SystemParserEngine) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "engine"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "config"}),
	}).Create(engine).Error
}
