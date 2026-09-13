package scheduler

import (
	"context"
	"sync"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const (
	ErrNoSlot        CodeError = "admission_deferred"
	ErrUnknownTask   CodeError = "unknown_task"
	ErrDuplicateTask CodeError = "duplicate_task"
)

type Task struct{ ID string }
type Scheduler struct {
	mu           sync.Mutex
	max, running int
	queue        []Task
	active       map[string]bool
}

func New(max int) *Scheduler {
	if max < 1 {
		max = 1
	}
	return &Scheduler{max: max, active: map[string]bool{}}
}
func (s *Scheduler) Add(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		return ErrUnknownTask
	}
	if s.active[t.ID] {
		return ErrDuplicateTask
	}
	for _, q := range s.queue {
		if q.ID == t.ID {
			return ErrDuplicateTask
		}
	}
	s.queue = append(s.queue, t)
	return nil
}
func (s *Scheduler) Next(ctx context.Context) (Task, error) {
	select {
	case <-ctx.Done():
		return Task{}, ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running >= s.max || len(s.queue) == 0 {
		return Task{}, ErrNoSlot
	}
	t := s.queue[0]
	s.queue = s.queue[1:]
	s.active[t.ID] = true
	s.running++
	return t, nil
}
func (s *Scheduler) Complete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active[id] {
		return ErrUnknownTask
	}
	delete(s.active, id)
	s.running--
	return nil
}
func (s *Scheduler) Running() int { s.mu.Lock(); defer s.mu.Unlock(); return s.running }
