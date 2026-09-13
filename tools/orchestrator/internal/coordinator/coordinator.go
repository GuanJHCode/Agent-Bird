package coordinator

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/host"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/scheduler"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"errors"
	"sync"
)

type Coordinator struct {
	db    *store.DB
	host  *host.Host
	queue *scheduler.Scheduler
	mu    sync.Mutex
}

func New(db *store.DB, spoolRoot string, slots int) (*Coordinator, error) {
	h, err := host.New(db, spoolRoot)
	if err != nil {
		return nil, err
	}
	return &Coordinator{db: db, host: h, queue: scheduler.New(slots)}, nil
}
func (c *Coordinator) SubmitRun(ctx context.Context, runID, controller string, origin contract.HostIdentity) error {
	return c.db.CreateRun(ctx, store.RunSpec{ID: runID, ControllerThread: controller, PlanRevision: 1, OriginContextID: origin.OriginContextID, OriginPID: origin.PID, OriginBirth: origin.Birth})
}
func (c *Coordinator) AddTask(ctx context.Context, runID, taskID string, deps []string, maxAttempts int) error {
	if err := c.db.CreateTask(ctx, store.TaskSpec{ID: taskID, RunID: runID, Dependencies: deps, MaxAttempts: maxAttempts}); err != nil {
		return err
	}
	return c.queue.Add(scheduler.Task{ID: taskID})
}
func (c *Coordinator) Submit(ctx context.Context, runID, controller, taskID string, origin contract.HostIdentity, deps []string, maxAttempts int) error {
	if err := c.SubmitRun(ctx, runID, controller, origin); err != nil {
		return err
	}
	return c.AddTask(ctx, runID, taskID, deps, maxAttempts)
}
func (c *Coordinator) Execute(ctx context.Context, runID, taskID, segmentID string, cmd process.Command) (contract.Result, error) {
	c.mu.Lock()
	t, err := c.queue.Next(ctx)
	c.mu.Unlock()
	if err != nil {
		return contract.Result{}, err
	}
	if t.ID != taskID {
		_ = c.queue.Add(t)
		return contract.Result{}, errors.New("task_order_conflict")
	}
	a, err := c.db.StartAttempt(ctx, taskID, segmentID)
	if err != nil {
		_ = c.queue.Complete(taskID)
		return contract.Result{}, err
	}
	r, err := c.host.Execute(ctx, a, runID, taskID, cmd)
	if r.Status != "unknown" {
		_ = c.queue.Complete(taskID)
	}
	return r, err
}
func (c *Coordinator) Collect(ctx context.Context, taskID string) ([]contract.Event, error) {
	a, err := c.db.LatestAttempt(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return c.host.Collect(ctx, a)
}
func (c *Coordinator) Ack(ctx context.Context, taskID string) error {
	a, err := c.db.LatestAttempt(ctx, taskID)
	if err != nil {
		return err
	}
	return c.host.Ack(ctx, a)
}
func (c *Coordinator) Resume(ctx context.Context, taskID, segmentID string, cmd process.Command) (contract.Result, error) {
	a, err := c.db.LatestAttempt(ctx, taskID)
	if err != nil {
		return contract.Result{}, err
	}
	next, err := c.db.StartSegment(ctx, a.ID, segmentID)
	if err != nil {
		return contract.Result{}, err
	}
	runID, err := c.db.RunID(ctx, taskID)
	if err != nil {
		return contract.Result{}, err
	}
	r, err := c.host.Execute(ctx, next, runID, taskID, cmd)
	return r, err
}
func (c *Coordinator) Ask(ctx context.Context, id, taskID string, revision int) error {
	return c.db.CreateQuestion(ctx, id, taskID, revision)
}
func (c *Coordinator) Answer(ctx context.Context, id string, revision int, answerHash string) error {
	return c.db.AnswerQuestion(ctx, id, revision, answerHash)
}
