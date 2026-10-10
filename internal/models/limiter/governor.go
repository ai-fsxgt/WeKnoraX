package limiter

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// The concurrency governor is process-wide, shared by every model-client layer
// that fronts a provider (chat, vlm). Keeping the singleton here — rather than
// inside one client package — lets all of them gate against the same limiter
// and per-model limit without importing each other. Wired once at startup (see
// container.registerModelConcurrencyLimiter) via SetGovernor.
var (
	governorMu  sync.RWMutex
	governor    ModelConcurrencyLimiter
	governorN   int
	modelLimits = map[string]int{}
)

// SetGovernor installs the process-wide background concurrency governor and the
// default per-model limit. Passing a nil limiter or a non-positive limit
// disables governance (all calls pass through). Safe to call at startup.
func SetGovernor(l ModelConcurrencyLimiter, limit int) {
	governorMu.Lock()
	defer governorMu.Unlock()
	governor = l
	governorN = limit
}

// SetGlobalLimit updates ONLY the process-wide default per-model limit,
// leaving the installed limiter backend intact. Used by the system-settings
// runtime bridge so an operator can retune model.max_concurrency without a
// restart. A non-positive value disables the default (models that carry their
// own MaxConcurrency still honour it).
func SetGlobalLimit(limit int) {
	governorMu.Lock()
	defer governorMu.Unlock()
	governorN = limit
}

// SetModelLimit records a model's own limit edited at runtime (model
// management). It overrides the limit captured in already-built clients, so
// running and queued background calls honour the edit without waiting for a
// new client. A non-positive value falls back to the process-wide default.
func SetModelLimit(modelID string, limit int) {
	governorMu.Lock()
	defer governorMu.Unlock()
	modelLimits[modelID] = limit
}

// resolveLimit returns the effective limit for a model: a runtime edit wins
// over the limit captured at client build time, and a non-positive model limit
// falls back to the process-wide default.
func resolveLimit(modelID string, modelLimit int) int {
	governorMu.RLock()
	defer governorMu.RUnlock()
	if limit, ok := modelLimits[modelID]; ok {
		modelLimit = limit
	}
	if modelLimit > 0 {
		return modelLimit
	}
	return governorN
}

// Gate acquires a per-model concurrency slot using the process-wide default
// limit. Equivalent to GateN(ctx, modelID, 0).
func Gate(ctx context.Context, modelID string) func() {
	return GateN(ctx, modelID, 0)
}

// GateN acquires a per-model concurrency slot when the call is a background task
// (see types.IsBackgroundTask) and a governor is installed. modelLimit is the
// model's own configured cap; a runtime edit via SetModelLimit overrides it and
// a value <= 0 means "fall back to the process-wide default" (governorN). The
// limit is re-resolved while waiting. It returns a release func that is ALWAYS
// safe to call: on the passthrough / fail-open paths it is a cheap no-op. The gate never
// blocks a call permanently — a limiter/Redis outage or a cancelled context
// fails open.
func GateN(ctx context.Context, modelID string, modelLimit int) func() {
	return GateNamedN(ctx, modelID, "", modelLimit)
}

func GateNamedN(ctx context.Context, modelID, modelName string, modelLimit int) func() {
	governorMu.RLock()
	l := governor
	governorMu.RUnlock()

	limit := func() int { return resolveLimit(modelID, modelLimit) }
	if l == nil || limit() <= 0 || !types.IsBackgroundTask(ctx) {
		return noop
	}
	if named, ok := l.(interface{ SetModelName(string, string) }); ok {
		named.SetModelName(modelID, modelName)
	}
	release, err := l.Acquire(ctx, modelID, limit)
	if err != nil || release == nil {
		return noop
	}
	return release
}

// RuntimeStats returns the semaphores observed by this process. Redis-backed
// limiters report cluster-wide active holders; waiting callers remain local to
// this instance by design.
func RuntimeStats(ctx context.Context) ([]RuntimeStat, bool, error) {
	governorMu.RLock()
	l := governor
	governorMu.RUnlock()
	inspector, ok := l.(runtimeInspectable)
	if !ok || inspector == nil {
		return []RuntimeStat{}, false, nil
	}
	stats, err := inspector.RuntimeStats(ctx)
	if stats == nil {
		stats = []RuntimeStat{}
	}
	return stats, true, err
}

// localLimiter is an in-process (single-node) counting semaphore keyed by
// model ID. It is the Lite-mode counterpart to the Redis limiter: Lite runs a
// single process with no Redis, so a shared distributed semaphore is neither
// available nor needed — but background ingestion can still burst the whole
// worker pool against one provider, so we still cap concurrency locally.
// Waiters poll like the Redis limiter so a changed limit applies to them.
type localLimiter struct {
	mu      sync.Mutex
	active  map[string]int
	tracked map[string]*trackedSemaphore
}

// NewLocalLimiter builds an in-process per-key concurrency limiter.
func NewLocalLimiter() ModelConcurrencyLimiter {
	return &localLimiter{active: make(map[string]int), tracked: make(map[string]*trackedSemaphore)}
}

func (l *localLimiter) Acquire(ctx context.Context, key string, limit func() int) (func(), error) {
	if l == nil || limit() <= 0 || key == "" {
		return noop, nil
	}

	l.mu.Lock()
	tracked := l.tracked[key]
	if tracked == nil {
		tracked = &trackedSemaphore{}
		l.tracked[key] = tracked
	}
	tracked.limit.Store(limit)
	l.mu.Unlock()
	tracked.waiting.Add(1)
	defer tracked.waiting.Add(-1)

	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		n := limit()
		if n <= 0 {
			// Governance was disabled while waiting.
			return noop, nil
		}
		l.mu.Lock()
		if l.active[key] < n {
			l.active[key]++
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					l.mu.Lock()
					l.active[key]--
					l.mu.Unlock()
				})
			}, nil
		}
		l.mu.Unlock()

		timer.Reset(defaultPollInterval)
		select {
		case <-ctx.Done():
			// Fail open on cancellation, mirroring the Redis limiter.
			return noop, nil
		case <-timer.C:
		}
	}
}

func (l *localLimiter) RuntimeStats(_ context.Context) ([]RuntimeStat, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats := make([]RuntimeStat, 0, len(l.tracked))
	for modelID, tracked := range l.tracked {
		name, _ := tracked.name.Load().(string)
		stats = append(stats, RuntimeStat{
			ModelID: modelID, Name: name, Active: int64(l.active[modelID]),
			Waiting: tracked.waiting.Load(), Limit: tracked.currentLimit(),
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].ModelID < stats[j].ModelID })
	return stats, nil
}

func (l *localLimiter) SetModelName(modelID, name string) {
	if modelID == "" || name == "" {
		return
	}
	l.mu.Lock()
	tracked := l.tracked[modelID]
	if tracked == nil {
		tracked = &trackedSemaphore{}
		l.tracked[modelID] = tracked
	}
	l.mu.Unlock()
	tracked.name.Store(name)
}
