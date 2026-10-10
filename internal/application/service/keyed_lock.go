package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/common/redislock"
	"github.com/redis/go-redis/v9"
)

const (
	serviceLockLease         = 30 * time.Second
	serviceLockRenewInterval = 10 * time.Second
)

type localKeyedLocks struct {
	mu    sync.Mutex
	locks map[string]*localKeyedLock
}

type localKeyedLock struct {
	mu   sync.Mutex
	refs int
}

var serviceLocalLocks = localKeyedLocks{
	locks: make(map[string]*localKeyedLock),
}

func withServiceLock(
	ctx context.Context,
	redisClient *redis.Client,
	key string,
	fn func(context.Context) error,
) error {
	if redisClient != nil {
		return redislock.WithRenewableLock(
			ctx,
			redisClient,
			key,
			serviceLockLease,
			serviceLockRenewInterval,
			fn,
		)
	}

	unlock := serviceLocalLocks.lock(key)
	defer unlock()
	return fn(ctx)
}

// detachedOperationContext preserves values but lets a mutation finish after
// its caller disconnects. When called inside a renewable lock, losing that
// lock still cancels the returned context.
func detachedOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detachedCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopOwnershipWatch := context.AfterFunc(redislock.OwnershipContext(ctx), cancel)
	return detachedCtx, func() {
		stopOwnershipWatch()
		cancel()
	}
}

func (l *localKeyedLocks) lock(key string) func() {
	entry := l.ref(key)
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.unref(key, entry)
	}
}

func (l *localKeyedLocks) tryLock(key string) (func(), bool) {
	entry := l.ref(key)
	if !entry.mu.TryLock() {
		l.unref(key, entry)
		return nil, false
	}
	return func() {
		entry.mu.Unlock()
		l.unref(key, entry)
	}, true
}

func (l *localKeyedLocks) ref(key string) *localKeyedLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.locks[key]
	if entry == nil {
		entry = &localKeyedLock{}
		l.locks[key] = entry
	}
	entry.refs++
	return entry
}

func (l *localKeyedLocks) unref(key string, entry *localKeyedLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, key)
	}
}

func knowledgeProcessingLockKey(tenantID uint64, knowledgeID string) string {
	return fmt.Sprintf("weknora:knowledge-processing:%d:%s", tenantID, knowledgeID)
}

func (s *knowledgeService) withKnowledgeProcessingLock(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	fn func(context.Context) error,
) error {
	return withServiceLock(ctx, s.redisClient, knowledgeProcessingLockKey(tenantID, knowledgeID), fn)
}

// tryKnowledgeProcessingLock lets an edit reject a still-running task instead
// of holding an HTTP request open until its model calls finish.
func (s *knowledgeService) tryKnowledgeProcessingLock(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	fn func(context.Context) error,
) (bool, error) {
	key := knowledgeProcessingLockKey(tenantID, knowledgeID)
	if s.redisClient != nil {
		return redislock.TryWithRenewableLock(ctx, s.redisClient, key,
			serviceLockLease, serviceLockRenewInterval, fn)
	}
	unlock, acquired := serviceLocalLocks.tryLock(key)
	if !acquired {
		return false, nil
	}
	defer unlock()
	return true, fn(ctx)
}
