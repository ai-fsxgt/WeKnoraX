package im

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/redis/go-redis/v9"
)

const (
	// defaultMaxQueueSize is the maximum number of pending QA requests in the queue.
	defaultMaxQueueSize = 50
	// defaultMaxPerUser limits how many requests a single user can have queued.
	defaultMaxPerUser = 3
	// defaultWorkers is the default number of concurrent QA workers.
	defaultWorkers = 5
	// queueTimeout is how long a request can wait in the queue before being discarded.
	queueTimeout = 60 * time.Second
	// redisQueueUserTTL is the TTL for per-user queue counters in Redis.
	redisQueueUserTTL = 5 * time.Minute
	// globalGateTTL is the TTL for the global active-worker counter in Redis.
	// Acts as a safety net: if all instances crash without decrementing, the
	// counter self-heals after this duration.
	globalGateTTL = 5 * time.Minute
	// globalGateRetryInterval is how long a worker waits before retrying when the
	// global concurrency limit is reached.
	globalGateRetryInterval = 500 * time.Millisecond
)

// qaRequest represents a QA request waiting in the queue.
type qaRequest struct {
	ctx       context.Context
	cancel    context.CancelFunc
	msg       *IncomingMessage
	session   *types.Session
	agent     *types.CustomAgent
	adapter   Adapter
	channel   *IMChannel
	channelID string
	bindingID string
	// serialize keeps unified participants' turns in order within this instance.
	serialize bool
	kbIDs     []string

	// tenant is used to resolve provider:// URLs in outbound replies (scheme-aware).
	tenant *types.Tenant

	// userKey identifies one binding context, including unified participants.
	userKey       string
	enqueuedAt    time.Time
	queueTimedOut bool
	// stopGeneration is the Redis cancellation marker observed at admission.
	stopGeneration string
}

// QueueMetrics exposes observable queue state.
type QueueMetrics struct {
	// Depth is the current number of requests waiting in the queue.
	Depth int
	// ActiveWorkers is the number of workers currently executing a QA request.
	ActiveWorkers int64
	// TotalEnqueued is the cumulative number of requests enqueued.
	TotalEnqueued int64
	// TotalProcessed is the cumulative number of requests dequeued and executed.
	TotalProcessed int64
	// TotalRejected is the cumulative number of requests rejected (queue full / per-user limit).
	TotalRejected int64
	// TotalTimeout is the cumulative number of requests discarded due to queue timeout.
	TotalTimeout int64
}

// qaQueue is a bounded, per-user-limited request queue with a fixed worker pool.
type qaQueue struct {
	mu             sync.Mutex
	cond           *sync.Cond
	queue          []*qaRequest
	maxSize        int
	maxPerUser     int
	workers        int
	perUser        map[string]int  // userKey → queued count
	activeContexts map[string]bool // unified contexts currently assigned to a worker
	closed         bool

	// redis is the optional Redis client for global per-user counting.
	// When nil, only local per-user limits are enforced.
	redis *redis.Client

	// globalMaxWorkers is the maximum number of QA requests executing
	// concurrently across all instances. 0 means no global limit.
	// Enforced via Redis INCR/DECR on RedisKeyGlobalGate.
	globalMaxWorkers int

	// metrics
	activeWorkers  atomic.Int64
	totalEnqueued  atomic.Int64
	totalProcessed atomic.Int64
	totalRejected  atomic.Int64
	totalTimeout   atomic.Int64

	// handler is called by workers to execute the QA request.
	handler func(req *qaRequest)
}

// newQAQueue creates a new bounded queue with the given worker count.
// globalMaxWorkers controls cross-instance concurrency (0 = no limit).
// redisClient may be nil for single-instance mode.
func newQAQueue(workers, maxSize, maxPerUser, globalMaxWorkers int, handler func(req *qaRequest), redisClient *redis.Client) *qaQueue {
	q := &qaQueue{
		queue:            make([]*qaRequest, 0, maxSize),
		maxSize:          maxSize,
		maxPerUser:       maxPerUser,
		workers:          workers,
		globalMaxWorkers: globalMaxWorkers,
		perUser:          make(map[string]int),
		activeContexts:   make(map[string]bool),
		redis:            redisClient,
		handler:          handler,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Start launches the worker goroutines and the metrics reporter. Call Stop to shut down.
func (q *qaQueue) Start(stopCh <-chan struct{}) {
	for i := 0; i < q.workers; i++ {
		go q.runWorker(i)
	}
	go q.metricsLoop(stopCh)
}

// Stop signals all workers to exit after draining.
func (q *qaQueue) Stop() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Enqueue adds a request to the queue. Returns the queue position (0-based)
// or an error if admission fails, the queue is full or the per-user limit is reached.
func (q *qaQueue) Enqueue(req *qaRequest) (position int, err error) {
	req.enqueuedAt = time.Now()
	if req.serialize && q.redis != nil {
		generation, err := q.redis.Get(req.ctx, RedisKeyStop+req.userKey).Result()
		if err != nil && err != redis.Nil {
			q.totalRejected.Add(1)
			return 0, fmt.Errorf("read conversation cancellation marker: %w", err)
		}
		req.stopGeneration = generation
	}
	// Check global per-user limit via Redis before acquiring local lock.
	if q.redis != nil {
		if err := q.redisCheckAndIncrUser(context.Background(), req.userKey); err != nil {
			q.totalRejected.Add(1)
			return 0, err
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		q.redisDecrUser(context.Background(), req.userKey)
		return 0, fmt.Errorf("queue is closed")
	}

	if len(q.queue) >= q.maxSize {
		q.redisDecrUser(context.Background(), req.userKey)
		q.totalRejected.Add(1)
		return 0, fmt.Errorf("queue full (%d/%d)", len(q.queue), q.maxSize)
	}

	// Local per-user check: only useful when Redis is nil (single-instance mode).
	// When Redis is available, redisCheckAndIncrUser already enforces the global
	// per-user limit across all instances, making this local check redundant.
	if q.redis == nil && q.perUser[req.userKey] >= q.maxPerUser {
		q.totalRejected.Add(1)
		return 0, fmt.Errorf("per-user queue limit reached (%d/%d)", q.perUser[req.userKey], q.maxPerUser)
	}

	q.queue = append(q.queue, req)
	if q.redis == nil {
		q.perUser[req.userKey]++
	}
	q.totalEnqueued.Add(1)
	pos := len(q.queue) - 1

	q.cond.Signal()
	return pos, nil
}

// Remove cancels and removes a queued request by userKey.
// Returns true if a request was found and removed.
func (q *qaQueue) Remove(userKey string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, req := range q.queue {
		if req.userKey == userKey {
			req.cancel()
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			if q.redis == nil {
				q.perUser[userKey]--
				if q.perUser[userKey] <= 0 {
					delete(q.perUser, userKey)
				}
			}
			q.redisDecrUser(context.Background(), userKey)
			return true
		}
	}
	return false
}

// Metrics returns a snapshot of the queue's observable state.
func (q *qaQueue) Metrics() QueueMetrics {
	q.mu.Lock()
	depth := len(q.queue)
	q.mu.Unlock()

	return QueueMetrics{
		Depth:          depth,
		ActiveWorkers:  q.activeWorkers.Load(),
		TotalEnqueued:  q.totalEnqueued.Load(),
		TotalProcessed: q.totalProcessed.Load(),
		TotalRejected:  q.totalRejected.Load(),
		TotalTimeout:   q.totalTimeout.Load(),
	}
}

func (q *qaQueue) runWorker(id int) {
	for {
		req := q.dequeue()
		if req == nil {
			return // queue closed
		}

		// Skip requests that have been cancelled or timed out while queued.
		if req.ctx.Err() != nil {
			q.totalTimeout.Add(1)
			q.finishRequest(req)
			continue
		}

		waitDuration := time.Since(req.enqueuedAt)
		if waitDuration > queueTimeout {
			q.totalTimeout.Add(1)
			logger.Warnf(req.ctx, "[IM] Queue timeout: user=%s waited=%s, discarding", req.msg.UserID, waitDuration)
			req.queueTimedOut = true
			q.handler(req)
			q.finishRequest(req)
			continue
		}

		logger.Infof(req.ctx, "[IM] Dequeued: worker=%d user=%s waited=%s depth=%d",
			id, req.msg.UserID, waitDuration, q.Metrics().Depth)

		// Acquire global concurrency slot (blocks until a slot opens or request is cancelled).
		if !q.acquireGlobalGate(req.ctx) {
			// Context cancelled while waiting for a global slot — treat as timeout.
			q.totalTimeout.Add(1)
			q.finishRequest(req)
			logger.Warnf(req.ctx, "[IM] Global gate wait cancelled: worker=%d user=%s", id, req.msg.UserID)
			req.cancel()
			continue
		}

		q.activeWorkers.Add(1)
		q.handler(req)
		q.activeWorkers.Add(-1)
		q.totalProcessed.Add(1)
		q.releaseGlobalGate()
		q.finishRequest(req)
	}
}

func (q *qaQueue) finishRequest(req *qaRequest) {
	q.redisDecrUser(context.Background(), req.userKey)
	if req.serialize {
		q.mu.Lock()
		delete(q.activeContexts, req.userKey)
		q.cond.Broadcast()
		q.mu.Unlock()
	}
}

func (q *qaQueue) dequeue() *qaRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		for i, req := range q.queue {
			if req.serialize && q.activeContexts[req.userKey] {
				continue
			}
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			if req.serialize {
				q.activeContexts[req.userKey] = true
			}
			if q.redis == nil {
				q.perUser[req.userKey]--
				if q.perUser[req.userKey] <= 0 {
					delete(q.perUser, req.userKey)
				}
			}
			return req
		}
		if q.closed && len(q.queue) == 0 {
			return nil
		}
		q.cond.Wait()
	}
}

// ── Redis global concurrency gate ────────────────────────────────────────────

// globalGateScript atomically increments the global active-worker counter and
// checks whether the limit is exceeded. Returns 1 if the slot was acquired, 0
// if the limit is reached. On success the caller MUST call releaseGlobalGate.
//
// KEYS[1] = RedisKeyGlobalGate
// ARGV[1] = max allowed concurrent workers
// ARGV[2] = TTL in milliseconds (safety net)
var globalGateScript = redis.NewScript(`
local key    = KEYS[1]
local maxW   = tonumber(ARGV[1])
local ttlMs  = tonumber(ARGV[2])

local count = redis.call('INCR', key)
redis.call('PEXPIRE', key, ttlMs)
if count <= maxW then
    return 1
end
redis.call('DECR', key)
return 0
`)

// acquireGlobalGate blocks until a global concurrency slot is available.
// Returns true if the slot was acquired, false if ctx was cancelled while waiting.
// When globalMaxWorkers is 0 or Redis is nil, it returns true immediately (no limit).
func (q *qaQueue) acquireGlobalGate(ctx context.Context) bool {
	if q.globalMaxWorkers <= 0 || q.redis == nil {
		return true
	}

	for {
		result, err := globalGateScript.Run(ctx, q.redis,
			[]string{RedisKeyGlobalGate},
			q.globalMaxWorkers, globalGateTTL.Milliseconds(),
		).Int64()
		if err != nil {
			// Redis error — skip global check to avoid blocking the worker.
			logger.Warnf(ctx, "[IM] Global gate Redis error (proceeding without limit): %v", err)
			return true
		}
		if result == 1 {
			return true
		}

		// Global limit reached — wait and retry.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(globalGateRetryInterval):
		}
	}
}

// releaseGlobalGate decrements the global active-worker counter.
func (q *qaQueue) releaseGlobalGate() {
	if q.globalMaxWorkers <= 0 || q.redis == nil {
		return
	}
	q.redis.Decr(context.Background(), RedisKeyGlobalGate)
}

// ── Redis global per-user counting ──────────────────────────────────────────

// redisCheckAndIncrUser atomically increments the global per-user counter and
// returns an error if the limit is exceeded. On success the caller MUST later
// call redisDecrUser to release the slot.
func (q *qaQueue) redisCheckAndIncrUser(ctx context.Context, userKey string) error {
	if q.redis == nil {
		return nil
	}
	key := RedisKeyQueueUser + userKey
	count, err := q.redis.Incr(ctx, key).Result()
	if err != nil {
		// Redis error — skip global check, rely on local limit.
		return nil
	}
	q.redis.Expire(ctx, key, redisQueueUserTTL)
	if count > int64(q.maxPerUser) {
		q.redis.Decr(ctx, key)
		return fmt.Errorf("global per-user queue limit reached (%d/%d)", count, q.maxPerUser)
	}
	return nil
}

// redisDecrUser releases one slot in the global per-user counter.
func (q *qaQueue) redisDecrUser(ctx context.Context, userKey string) {
	if q.redis == nil {
		return
	}
	key := RedisKeyQueueUser + userKey
	q.redis.Decr(ctx, key)
}

// ── Metrics logging ─────────────────────────────────────────────────────────

const metricsLogInterval = 30 * time.Second

// metricsLoop periodically logs queue metrics for operational visibility.
func (q *qaQueue) metricsLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(metricsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m := q.Metrics()
			// Only log when there is activity to avoid noise.
			if m.Depth > 0 || m.ActiveWorkers > 0 {
				logger.Infof(context.Background(),
					"[IM] Queue metrics: depth=%d active_workers=%d enqueued=%d processed=%d rejected=%d timeout=%d",
					m.Depth, m.ActiveWorkers, m.TotalEnqueued, m.TotalProcessed, m.TotalRejected, m.TotalTimeout)
			}
		case <-stopCh:
			return
		}
	}
}
