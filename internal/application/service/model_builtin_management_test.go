package service

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func builtinModelContext(systemAdmin bool) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
}

func TestUpdateBuiltinModel_RequiresSystemAdmin(t *testing.T) {
	stored := &types.Model{
		ID: "builtin-chat", TenantID: 10000, IsBuiltin: true,
		ManagedBy: types.BuiltinModelManagedBy,
	}
	updated := false
	svc := NewModelService(&stubModelRepoForDelete{
		model: stored,
		update: func(*types.Model) error {
			updated = true
			return nil
		},
	}, nil, nil, nil, nil, nil)

	err := svc.UpdateModel(builtinModelContext(false), &types.Model{ID: stored.ID}, true)
	require.Error(t, err)
	appErr, ok := apperrors.IsAppError(err)
	require.True(t, ok)
	assert.Equal(t, apperrors.ErrForbidden, appErr.Code)
	assert.False(t, updated)
}

func TestUpdateBuiltinModel_SystemAdminCreatesRuntimeOverride(t *testing.T) {
	stored := &types.Model{
		ID: "builtin-chat", TenantID: 10000, IsBuiltin: true,
		ManagedBy: types.BuiltinModelManagedBy,
	}
	var saved *types.Model
	svc := NewModelService(&stubModelRepoForDelete{
		model: stored,
		update: func(model *types.Model) error {
			copy := *model
			saved = &copy
			return nil
		},
	}, nil, nil, nil, nil, nil)

	input := &types.Model{ID: stored.ID, Name: "edited"}
	require.NoError(t, svc.UpdateModel(builtinModelContext(true), input, true))
	require.NotNil(t, saved)
	assert.Equal(t, uint64(10000), saved.TenantID)
	assert.True(t, saved.IsBuiltin)
	assert.Empty(t, saved.ManagedBy, "UI edit must stop later YAML reconciliation")
}

func TestUpdateModelCredentialPreservation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		preserve bool
	}{{"configuration", true}, {"initialization", false}} {
		t.Run(tt.name, func(t *testing.T) {
			stored := &types.Model{
				ID: "model-credentials", TenantID: 10000,
				Parameters: types.ModelParameters{APIKey: "latest-key", AppSecret: "latest-secret"},
			}
			var saved *types.Model
			svc := NewModelService(&stubModelRepoForDelete{
				model: stored,
				update: func(model *types.Model) error {
					updated := *model
					saved = &updated
					return nil
				},
			}, nil, nil, nil, nil, nil)
			input := &types.Model{
				ID: stored.ID,
				Parameters: types.ModelParameters{
					APIKey: "input-key", AppSecret: "input-secret", MaxConcurrency: 3,
				},
			}
			require.NoError(t, svc.UpdateModel(builtinModelContext(true), input, tt.preserve))
			if tt.preserve {
				assert.Equal(t, "latest-key", saved.Parameters.APIKey)
				assert.Equal(t, "latest-secret", saved.Parameters.AppSecret)
			} else {
				assert.Equal(t, "input-key", saved.Parameters.APIKey)
				assert.Equal(t, "input-secret", saved.Parameters.AppSecret)
			}
			assert.Equal(t, 3, saved.Parameters.MaxConcurrency)
		})
	}

	t.Run("independent services preserve concurrent edits", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "models.db") + "?_busy_timeout=5000&_journal_mode=WAL"
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, db.AutoMigrate(&types.Model{}))
		repo := repository.NewModelRepository(db)
		ctx := builtinModelContext(true)
		stored := &types.Model{
			ID: "concurrent-model", TenantID: 7,
			Parameters: types.ModelParameters{APIKey: "original-key", AppSecret: "original-secret"},
		}
		require.NoError(t, repo.Create(ctx, stored))
		svc1 := NewModelService(repo, nil, nil, nil, nil, nil)
		db2, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		svc2 := NewModelService(repository.NewModelRepository(db2), nil, nil, nil, nil, nil)
		db3, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		svc3 := NewModelService(repository.NewModelRepository(db3), nil, nil, nil, nil, nil)
		for _, conn := range []*gorm.DB{db, db2, db3} {
			sqlDB, err := conn.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
		}

		for i := range 10 {
			key := fmt.Sprintf("updated-key-%d", i)
			secret := fmt.Sprintf("updated-secret-%d", i)
			input := &types.Model{
				ID: stored.ID, TenantID: stored.TenantID,
				Name:       fmt.Sprintf("edited-%d", i),
				Parameters: types.ModelParameters{APIKey: "stale-key", AppSecret: "stale-secret", MaxConcurrency: i},
			}
			start := make(chan struct{})
			results := make(chan error, 3)
			go func() {
				<-start
				results <- svc1.UpdateModel(ctx, input, true)
			}()
			go func() {
				<-start
				_, err := svc2.UpdateModelCredentials(ctx, stored.ID, &key, nil)
				results <- err
			}()
			go func() {
				<-start
				_, err := svc3.UpdateModelCredentials(ctx, stored.ID, nil, &secret)
				results <- err
			}()
			close(start)
			for range 3 {
				require.NoError(t, <-results)
			}
			updated, err := repo.GetByID(ctx, stored.TenantID, stored.ID)
			require.NoError(t, err)
			assert.Equal(t, input.Name, updated.Name)
			assert.Equal(t, i, updated.Parameters.MaxConcurrency)
			assert.Equal(t, key, updated.Parameters.APIKey)
			assert.Equal(t, secret, updated.Parameters.AppSecret)

			for _, field := range []string{"api_key", "app_secret"} {
				go func() { results <- svc1.UpdateModel(ctx, input, true) }()
				go func() { results <- svc2.ClearModelCredential(ctx, stored.ID, field) }()
				for range 2 {
					require.NoError(t, <-results)
				}
				updated, err = repo.GetByID(ctx, stored.TenantID, stored.ID)
				require.NoError(t, err)
				assert.Empty(t, updated.Parameters.APIKey)
				if field == "app_secret" {
					assert.Empty(t, updated.Parameters.AppSecret)
				} else {
					assert.Equal(t, secret, updated.Parameters.AppSecret)
				}
				assert.Equal(t, i, updated.Parameters.MaxConcurrency)
			}
		}
	})
}

func TestUpdateBuiltinModelCredentials_SystemAdminOnly(t *testing.T) {
	newKey := "sk-new"

	t.Run("tenant admin denied", func(t *testing.T) {
		stored := &types.Model{ID: "builtin-chat", TenantID: 10000, IsBuiltin: true}
		svc := NewModelService(&stubModelRepoForDelete{model: stored}, nil, nil, nil, nil, nil)
		_, err := svc.UpdateModelCredentials(builtinModelContext(false), stored.ID, &newKey, nil)
		require.Error(t, err)
		appErr, ok := apperrors.IsAppError(err)
		require.True(t, ok)
		assert.Equal(t, apperrors.ErrForbidden, appErr.Code)
	})

	t.Run("system admin saves runtime override", func(t *testing.T) {
		stored := &types.Model{
			ID: "builtin-chat", TenantID: 10000, IsBuiltin: true,
			ManagedBy: types.BuiltinModelManagedBy,
		}
		var saved *types.Model
		svc := NewModelService(&stubModelRepoForDelete{
			model: stored,
			update: func(model *types.Model) error {
				copy := *model
				saved = &copy
				return nil
			},
		}, nil, nil, nil, nil, nil)

		updated, err := svc.UpdateModelCredentials(
			builtinModelContext(true), stored.ID, &newKey, nil,
		)
		require.NoError(t, err)
		assert.Equal(t, newKey, updated.Parameters.APIKey)
		require.NotNil(t, saved)
		assert.Empty(t, saved.ManagedBy)
	})
}
