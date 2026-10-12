package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TaskInspector abstracts queue inspection / cancellation against the
// task backend. It is best-effort: implementations may scan a finite
// number of tasks per call and return whatever count they could
// affect. Lite mode (no Redis) ships a no-op implementation because
// SyncTaskExecutor dispatches inline goroutines that cannot be
// dequeued before they start.
//
// Use cases today: user-initiated cancel of an in-progress knowledge
// parse, which must remove downstream multimodal / post-process /
// question / summary tasks already enqueued against the same
// knowledge_id, plus signal active workers to stop at their next
// checkpoint.
type TaskInspector interface {
	// CancelTasksForKnowledge removes pending/scheduled/retry tasks
	// whose payload references the given knowledge ID, and signals
	// active workers running such tasks to stop. Returns rough
	// counts of (deletedFromQueue, activeCancelled) for observability.
	// Errors are returned but callers should treat the operation as
	// best-effort: the row-level abort flag remains the source of
	// truth, this just prevents wasted work.
	CancelTasksForKnowledge(ctx context.Context, knowledgeID string) (deleted int, cancelled int, err error)

	// QueuedKnowledgeIDs returns the knowledge IDs referenced by any
	// pending / scheduled / retry / active task in the queue backend. It is
	// the read-only counterpart of CancelTasksForKnowledge: the housekeeping
	// sweep calls it once per scan pass before flipping long-idle
	// "processing"/"finalizing" rows to "failed", so it can tell a genuinely
	// orphaned row (no task anywhere) from one whose enrichment subtasks are
	// merely backlogged behind a busy queue (no span heartbeat yet because
	// no worker has picked them up).
	//
	// It snapshots each queue's live task IDs atomically, so state transitions
	// cannot move tasks out of the read. Cost depends on live task count,
	// not on candidates, historical tasks, or unrelated Redis keys.
	// On backend error it returns the error instead of an
	// incomplete set; callers decide the fail-safe direction. Lite mode
	// (no Redis) returns an empty set — inline executors never queue, so the
	// span/updated_at checks remain authoritative there.
	QueuedKnowledgeIDs(ctx context.Context) (map[string]struct{}, error)

	// QueueStats returns a read-only depth snapshot for every queue this
	// application enqueues into, for the System Admin runtime dashboard.
	//
	// The `supported` bool reports whether queue inspection is available
	// on the current backend: true in asynq/Redis mode, false in Lite
	// mode (no Redis) where tasks run inline and no queue exists. Callers
	// use it to render an "unavailable in this deployment" state rather
	// than an empty table.
	//
	// Queues that have not received tasks are surfaced as zeroed rows.
	// Backend errors are returned instead of an incomplete snapshot.
	QueueStats(ctx context.Context) (stats []types.QueueStat, supported bool, err error)

	// WorkerServerStats returns live asynq server heartbeats across all
	// replicas. The runtime dashboard uses them to aggregate actual cluster
	// capacity and busy workers for each configured pool.
	WorkerServerStats(ctx context.Context) (stats []types.WorkerServerStat, supported bool, err error)
}

// KnowledgeBaseTaskCanceller is the optional queue-cleanup capability used
// when a knowledge base is deleted. It is separate from TaskInspector so
// lightweight test doubles and queue backends that cannot inspect tasks do
// not need to implement knowledge-base-wide scanning.
type KnowledgeBaseTaskCanceller interface {
	// CancelTasksForKnowledgeBase removes pending/scheduled/retry tasks
	// associated with kbID and signals matching active workers to stop.
	// knowledgeIDs covers batch tasks whose payload references documents but
	// does not carry the parent knowledge-base ID. dataSourceIDs covers sync
	// tasks whose payload only identifies a data source.
	CancelTasksForKnowledgeBase(
		ctx context.Context,
		kbID string,
		knowledgeIDs []string,
		dataSourceIDs []string,
	) (deleted int, cancelled int, err error)
}

// RuntimeTaskInspector is the optional operator surface implemented by queue
// backends that retain inspectable task state. It is separate from
// TaskInspector so Lite mode and light-weight tests do not need to implement
// queue mutations.
type RuntimeTaskInspector interface {
	ListRuntimeTasks(
		ctx context.Context,
		queue string,
		state types.RuntimeTaskState,
		cursor string,
		pageSize int,
	) (page types.RuntimeTaskPage, supported bool, err error)
	GetRuntimeTask(ctx context.Context, queue, taskID string) (task *types.RuntimeTaskInfo, supported bool, err error)
	RunRuntimeTask(ctx context.Context, queue, taskID string) (supported bool, err error)
	DeleteRuntimeTask(ctx context.Context, queue, taskID string) (supported bool, err error)
	// ForceDeleteRuntimeTask removes a queue record without checking the
	// operator-facing AllowedActions. Used when the business row is already
	// gone but a retry/pending task survived (orphan cleanup).
	ForceDeleteRuntimeTask(ctx context.Context, queue, taskID string) (supported bool, err error)
	// PurgeArchivedRuntimeTasks removes every archived (finally-failed) task
	// from one queue in a single call and returns how many records were
	// deleted. It only ever touches the archived (dead-letter) set, so live
	// pending/active/scheduled/retry tasks are never affected. Business state
	// is intentionally left untouched: archived tasks have already had their
	// document status flipped to "failed" on their last retry, mirroring the
	// semantics of deleting archived records one by one.
	PurgeArchivedRuntimeTasks(ctx context.Context, queue string) (deleted int, supported bool, err error)
}
