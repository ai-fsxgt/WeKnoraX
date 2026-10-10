package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/redis/go-redis/v9"
)

// withManualKnowledgeProcessing serializes manual tasks with content edits.
// Non-manual documents retain their existing processing path.
func withManualKnowledgeProcessing(
	ctx context.Context,
	redisClient *redis.Client,
	getKnowledge func(context.Context, string) (*types.Knowledge, error),
	tenantID uint64,
	knowledgeID string,
	version int,
	fn func(context.Context) error,
) error {
	knowledge, err := getKnowledge(ctx, knowledgeID)
	if err != nil && !errors.Is(err, repository.ErrKnowledgeNotFound) {
		return err
	}
	if knowledge == nil || !knowledge.IsManual() {
		if version > 0 {
			return nil
		}
		// Preserve non-manual handlers' existing missing-row cleanup.
		return fn(ctx)
	}
	if knowledge.TenantID != tenantID {
		return nil
	}
	current, err := manualKnowledgeProcessingCurrent(knowledge, version)
	if err != nil || !current {
		return err
	}

	return withServiceLock(ctx, redisClient, knowledgeProcessingLockKey(tenantID, knowledgeID),
		func(lockCtx context.Context) error {
			// The task may have waited behind an edit or a newer task. Re-read
			// inside the lock before any cleanup, indexing or state write.
			knowledge, err := getKnowledge(lockCtx, knowledgeID)
			if err != nil {
				if errors.Is(err, repository.ErrKnowledgeNotFound) {
					return nil
				}
				return err
			}
			if knowledge == nil || knowledge.TenantID != tenantID {
				return nil
			}
			current, err := manualKnowledgeProcessingCurrent(knowledge, version)
			if err != nil {
				return err
			}
			if !current {
				logger.Infof(lockCtx, "Skipping stale or aborted manual task: knowledge=%s version=%d",
					knowledgeID, version)
				return nil
			}
			lockCtx = types.WithManualKnowledgeVersion(lockCtx, knowledgeID, version)
			err = fn(lockCtx)
			if errors.Is(err, types.ErrManualKnowledgeSuperseded) {
				return nil
			}
			return err
		})
}

// withManualKnowledgeSubtask also releases a counted enrichment slot when
// the final attempt fails before acquiring the document lock.
func withManualKnowledgeSubtask(
	ctx context.Context,
	redisClient *redis.Client,
	repo interfaces.KnowledgeRepository,
	tenantID uint64,
	knowledgeID string,
	version int,
	source string,
	fn func(context.Context) error,
) error {
	started := false
	err := withManualKnowledgeProcessing(ctx, redisClient, repo.GetKnowledgeByIDOnly,
		tenantID, knowledgeID, version, func(lockCtx context.Context) error {
			started = true
			return fn(lockCtx)
		})
	if err != nil && !started && version > 0 {
		ctx = types.WithManualKnowledgeVersion(ctx, knowledgeID, version)
		finalizeSubtaskDetached(ctx, repo, knowledgeID, source, err, false, isFinalAsynqAttempt(ctx))
	}
	return err
}

func manualKnowledgeProcessingCurrent(knowledge *types.Knowledge, version int) (bool, error) {
	if !knowledge.IsManual() {
		return false, nil
	}
	meta, err := knowledge.ManualMetadata()
	if err != nil {
		return false, fmt.Errorf("read manual processing version: %w", err)
	}
	if meta == nil {
		return false, fmt.Errorf("manual metadata missing for knowledge %s", knowledge.ID)
	}
	return version == meta.Version && knowledge.ParseStatus != types.ParseStatusCancelled &&
		knowledge.ParseStatus != types.ParseStatusDeleting &&
		knowledge.ParseStatus != types.ManualKnowledgeStatusDraft, nil
}
