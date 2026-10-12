package host

import (
	"context"
	"sync"
)

// ConfigureConcurrency permits at most 32 active executions. One original
// launch and one stop per execution must fit even while both lanes are blocked.
// Counts include executing jobs and are shared across every transport reader.
const ownerCommandLaneLimit = 32

type ownerCommandJob struct {
	kind       string
	generation uint64
	execute    func() error
	finish     func()
	release    func() // called by finish while execution.mu is held
	once       sync.Once
	result     error
}

func (j *ownerCommandJob) run() error {
	j.once.Do(func() {
		defer j.finish()
		j.result = j.execute()
	})
	return j.result
}

type ownerCommandDispatcher struct {
	lifecycle                  *ownerRuntimeLifecycle
	launch, stop               chan *ownerCommandJob
	launchDone, stopDone       chan struct{}
	launchPending, stopPending int  // guarded by execution.mu
	closed                     bool // guarded by execution.mu
}

func newOwnerCommandDispatcher(r *ownerRuntimeLifecycle) *ownerCommandDispatcher {
	d := &ownerCommandDispatcher{lifecycle: r, launch: make(chan *ownerCommandJob, ownerCommandLaneLimit), stop: make(chan *ownerCommandJob, ownerCommandLaneLimit), launchDone: make(chan struct{}), stopDone: make(chan struct{})}
	go d.work(d.launch, d.launchDone)
	go d.work(d.stop, d.stopDone)
	context.AfterFunc(r.execution.run.ctx, d.close)
	return d
}

// enqueue is called only with execution.mu held, after full authentication.
// Its bounded send cannot wait: pending includes this lane's executing job.
func (d *ownerCommandDispatcher) enqueue(job *ownerCommandJob) bool {
	if d.closed {
		return false
	}
	queue, pending := d.launch, &d.launchPending
	if job.kind == "stop" {
		queue, pending = d.stop, &d.stopPending
	}
	if *pending >= ownerCommandLaneLimit {
		return false
	}
	*pending++
	job.release = func() { *pending-- }
	queue <- job
	return true
}

func (d *ownerCommandDispatcher) close() {
	e := d.lifecycle.execution
	e.mu.Lock()
	defer e.mu.Unlock()
	// Also cover a caller canceling the private execution context directly.
	// Do not cancel workers here: beginStop first registers the shared TERM.
	e.stopping, e.active, e.admitSignedLaunch = true, false, false
	if !d.closed {
		d.closed = true
		close(d.launch)
		close(d.stop)
	}
}

func (d *ownerCommandDispatcher) work(queue <-chan *ownerCommandJob, done chan<- struct{}) {
	defer close(done)
	// Never discard an authenticated job on cancellation. Its durable grant
	// must settle as noStart/duplicate/UNKNOWN through the original path.
	for job := range queue {
		err := job.run()
		switch ownerCommandDisposition(err) {
		case ownerCommandOK, ownerCommandStale, ownerCommandStopped:
		case ownerCommandRecover:
			d.lifecycle.notice(ownerRuntimeNotice{generation: job.generation, cause: err})
		case ownerCommandInvalid:
			// The transport was authenticated before enqueue; a later failure
			// belongs to local execution, not to an obsolete reader generation.
			d.lifecycle.cancel(d.lifecycle.execution.failAdmission(err))
		default:
			d.lifecycle.cancel(err)
		}
	}
}
