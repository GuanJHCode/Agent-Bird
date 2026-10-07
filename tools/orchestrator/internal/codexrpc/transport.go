package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"unicode/utf8"
)

type jobStage uint8

const (
	queued jobStage = iota
	writing
	written
	finished
	dropped
)

type outcome struct {
	result json.RawMessage
	err    error
}
type callState struct {
	id       string
	job      *writeJob
	reply    chan outcome
	response *outcome
}
type writeJob struct {
	ctx     context.Context
	frame   []byte
	stage   jobStage
	stop    func() bool
	call    *callState
	request *Request
	reply   chan error
}

type closeWriter interface {
	CloseWrite() error
}

// Client owns one reader, one writer, and a join goroutine. There are no
// per-operation dispatch goroutines; context callbacks only cancel owned jobs.
type Client struct {
	stream        io.ReadWriteCloser
	limits        Limits
	mu            sync.Mutex
	err           error
	writeClosed   bool
	readEOF       bool
	closingWrite  bool
	closeWrite    chan struct{}
	closeWriteErr error
	next          uint64
	pending       map[string]*callState
	remote        map[ID]*Request
	consumed      map[ID]struct{}
	responders    int
	jobs          map[*writeJob]struct{}
	out           chan *writeJob
	events        chan Event
	done, joined  chan struct{}
	wg            sync.WaitGroup
	callbacks     sync.WaitGroup
}

// New takes ownership only on success. The supplied Close must be idempotent,
// return promptly, and interrupt pending Read and Write. Closing this stream
// does not prove worker exit. An OS pipe adapter needs separate OS acceptance.
func New(ctx context.Context, stream io.ReadWriteCloser, limits Limits) (*Client, error) {
	if ctx == nil || stream == nil {
		return nil, ErrConstructor
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	c := &Client{stream: stream, limits: l, pending: make(map[string]*callState), remote: make(map[ID]*Request), consumed: make(map[ID]struct{}), jobs: make(map[*writeJob]struct{}), out: make(chan *writeJob, writerQueueSize), events: make(chan Event, l.Events), done: make(chan struct{}), joined: make(chan struct{})}
	stopContext := c.watch(ctx, func() { c.fail(ctx.Err()) })
	c.wg.Add(2)
	go c.readPump()
	go c.writePump()
	go func() {
		c.wg.Wait()
		stopContext()
		c.callbacks.Wait()
		close(c.events)
		close(c.joined)
	}()
	return c, nil
}

func (c *Client) Events() <-chan Event  { return c.events }
func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) Err() error            { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *Client) ReadEOF() bool         { c.mu.Lock(); defer c.mu.Unlock(); return c.readEOF }

// CloseWrite freezes new outbound frames and closes only the owned stream's
// write side. A natural reader EOF wins a race with the underlying close.
func (c *Client) CloseWrite() error {
	c.mu.Lock()
	if c.closingWrite {
		done := c.closeWrite
		c.mu.Unlock()
		<-done
		c.mu.Lock()
		err := c.closeWriteErr
		c.mu.Unlock()
		return err
	}
	if c.writeClosed {
		err := c.closeWriteErr
		c.mu.Unlock()
		return err
	}
	if c.err != nil {
		if c.readEOF && errors.Is(c.err, io.EOF) {
			c.writeClosed = true
			c.mu.Unlock()
			return nil
		}
		err := c.err
		c.mu.Unlock()
		return err
	}
	if len(c.jobs) != 0 || len(c.pending) != 0 {
		c.mu.Unlock()
		return ErrPending
	}
	c.writeClosed = true
	c.closingWrite = true
	c.closeWrite = make(chan struct{})
	done := c.closeWrite
	c.mu.Unlock()

	writer, ok := c.stream.(closeWriter)
	var closeErr error
	if !ok {
		closeErr = ErrIO
	} else if err := writer.CloseWrite(); err != nil {
		closeErr = ErrIO
	}

	c.mu.Lock()
	if closeErr != nil && c.readEOF && errors.Is(c.err, io.EOF) {
		closeErr = nil
	}
	c.closeWriteErr = closeErr
	c.closingWrite = false
	close(done)
	c.mu.Unlock()
	if closeErr != nil {
		c.fail(closeErr)
	}
	return closeErr
}

// Close interrupts the stream and joins all owned pump/dispatch goroutines.
func (c *Client) Close() error { c.fail(ErrClosed); <-c.joined; return nil }

// Count both registered and running context callbacks. Admission holds mu,
// so none is registered after terminal shutdown begins joining.
func (c *Client) watch(ctx context.Context, callback func()) func() bool {
	c.callbacks.Add(1)
	stop := context.AfterFunc(ctx, func() {
		defer c.callbacks.Done()
		callback()
	})
	return func() bool {
		stopped := stop()
		if stopped {
			c.callbacks.Done()
		}
		return stopped
	}
}

func (c *Client) fail(err error) {
	if err == nil {
		err = ErrClosed
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	for job := range c.jobs {
		c.finishLocked(job, outcome{err: err}, dropped)
	}
	close(c.done)
	c.mu.Unlock()
	_ = c.stream.Close()
}

func (c *Client) finishLocked(job *writeJob, value outcome, stage jobStage) {
	if job.stage == finished || job.stage == dropped {
		return
	}
	if job.request != nil && job.stage == queued {
		job.request.responding = false
		c.responders--
	}
	job.stage = stage
	job.frame = nil
	delete(c.jobs, job)
	if job.stop != nil {
		job.stop()
	}
	if job.call != nil {
		delete(c.pending, job.call.id)
		job.call.response = nil
		job.call.reply <- value
	} else {
		job.reply <- value.err
	}
}

func (c *Client) cancelJob(job *writeJob) {
	c.mu.Lock()
	if job.stage == finished || job.stage == dropped {
		c.mu.Unlock()
		return
	}
	err := job.ctx.Err()
	if job.stage == queued {
		c.finishLocked(job, outcome{err: err}, dropped)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	// A dispatch may have executed. Do not abandon it and reuse the stream.
	c.fail(err)
}

func (c *Client) enqueueLocked(job *writeJob) error {
	c.jobs[job] = struct{}{}
	job.stop = c.watch(job.ctx, func() { c.cancelJob(job) })
	select {
	case c.out <- job:
		return nil
	default:
		c.finishLocked(job, outcome{err: ErrWriterLimit}, dropped)
		return ErrWriterLimit
	}
}

func (c *Client) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if ctx == nil {
		return nil, ErrPayload
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := outboundValid(method, params, c.limits.FrameBytes); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if c.writeClosed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if len(c.pending) >= c.limits.LocalCalls {
		c.mu.Unlock()
		return nil, ErrPendingLimit
	}
	c.next++
	if c.next == 0 {
		c.mu.Unlock()
		c.fail(ErrPendingLimit)
		return nil, ErrPendingLimit
	}
	id := "local-" + strconv.FormatUint(c.next, 10)
	body, err := encodedFrame(struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{id, method, params}, c.limits.FrameBytes)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	call := &callState{id: id, reply: make(chan outcome, 1)}
	job := &writeJob{ctx: ctx, frame: body, call: call}
	call.job = job
	c.pending[id] = call
	err = c.enqueueLocked(job)
	c.mu.Unlock()
	if err != nil {
		c.fail(err)
		return nil, err
	}
	value := <-call.reply
	return value.result, value.err
}

func (c *Client) Notify(ctx context.Context, method string, params json.RawMessage) error {
	if ctx == nil {
		return ErrPayload
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := outboundValid(method, params, c.limits.FrameBytes); err != nil {
		return err
	}
	body, err := encodedFrame(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}{method, params}, c.limits.FrameBytes)
	if err != nil {
		return err
	}
	return c.send(ctx, body, nil)
}

func (c *Client) Respond(ctx context.Context, request *Request, reply Reply) error {
	if ctx == nil {
		return ErrPayload
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request == nil || request.owner != c {
		return ErrAuthority
	}
	if (reply.Result != nil) == (reply.Error != nil) {
		return ErrPayload
	}
	if err := rawValid(reply.Result, c.limits.FrameBytes); err != nil {
		return err
	}
	var value any
	if reply.Error != nil {
		if len(reply.Error.Message) > c.limits.FrameBytes {
			return ErrFrameLimit
		}
		if !utf8.ValidString(reply.Error.Message) {
			return ErrPayload
		}
		if err := rawValid(reply.Error.Data, c.limits.FrameBytes); err != nil {
			return err
		}
		value = struct {
			ID    ID        `json:"id"`
			Error *RPCError `json:"error"`
		}{request.id, reply.Error}
	} else {
		value = struct {
			ID     ID              `json:"id"`
			Result json.RawMessage `json:"result"`
		}{request.id, reply.Result}
	}
	body, err := encodedFrame(value, c.limits.FrameBytes)
	if err != nil {
		return err
	}
	return c.send(ctx, body, request)
}

func (c *Client) send(ctx context.Context, body []byte, request *Request) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if c.writeClosed {
		c.mu.Unlock()
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	if request != nil {
		if _, exists := c.consumed[request.id]; exists {
			c.mu.Unlock()
			return ErrResponded
		}
		if c.remote[request.id] != request {
			c.mu.Unlock()
			return ErrAuthority
		}
		if request.responding {
			c.mu.Unlock()
			return ErrResponded
		}
		if len(c.consumed)+c.responders >= c.limits.ConsumedRequests {
			c.mu.Unlock()
			c.fail(ErrConsumedLimit)
			return ErrConsumedLimit
		}
		request.responding = true
		c.responders++
	}
	job := &writeJob{ctx: ctx, frame: body, request: request, reply: make(chan error, 1)}
	err := c.enqueueLocked(job)
	c.mu.Unlock()
	if err != nil {
		c.fail(err)
		return err
	}
	return <-job.reply
}

func (c *Client) writePump() {
	defer c.wg.Done()
	for {
		var job *writeJob
		select {
		case <-c.done:
			return
		case job = <-c.out:
		}
		c.mu.Lock()
		if c.err != nil {
			c.mu.Unlock()
			return
		}
		if job.stage == dropped {
			c.mu.Unlock()
			continue
		}
		if err := job.ctx.Err(); err != nil {
			c.finishLocked(job, outcome{err: err}, dropped)
			c.mu.Unlock()
			continue
		}
		job.stage = writing
		if job.request != nil {
			delete(c.remote, job.request.id)
			c.consumed[job.request.id] = struct{}{}
			c.responders--
		}
		body := job.frame
		c.mu.Unlock()
		for len(body) > 0 {
			n, err := c.stream.Write(body)
			if n < 0 || n > len(body) || (n == 0 && err == nil) {
				c.fail(ErrIO)
				return
			}
			body = body[n:]
			if err != nil {
				c.fail(ErrIO)
				return
			}
		}
		c.mu.Lock()
		if job.stage != finished && job.stage != dropped {
			job.frame = nil
			if job.call != nil {
				// A response may arrive while the real Write is still in flight.
				// Keep the original job and cancellation until full write success,
				// then deliver only if the original context is still live.
				if err := job.ctx.Err(); err != nil {
					c.mu.Unlock()
					c.fail(err)
					return
				}
				job.stage = written
				if job.call.response != nil {
					c.finishLocked(job, *job.call.response, finished)
				}
			} else {
				c.finishLocked(job, outcome{}, finished)
			}
		}
		c.mu.Unlock()
	}
}

func (c *Client) readPump() {
	defer c.wg.Done()
	reader := bufio.NewReaderSize(c.stream, 4096)
	var body []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(body)+len(part) > c.limits.FrameBytes+1 {
			c.fail(ErrFrameLimit)
			return
		}
		body = append(body, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.mu.Lock()
				c.readEOF = true
				c.mu.Unlock()
			}
			if len(body) > c.limits.FrameBytes {
				c.fail(ErrFrameLimit)
			} else if len(body) > 0 {
				c.fail(ErrIncompleteFrame)
			} else if errors.Is(err, io.EOF) {
				c.fail(io.EOF)
			} else {
				c.fail(ErrIO)
			}
			return
		}
		body = body[:len(body)-1]
		if len(body) > c.limits.FrameBytes {
			c.fail(ErrFrameLimit)
			return
		}
		f, err := decodeFrame(body)
		body = nil
		if err != nil {
			c.fail(err)
			return
		}
		if err = c.accept(f); err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) accept(f frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if f.kind == resultFrame || f.kind == errorFrame {
		if !f.id.isString {
			return ErrProtocol
		}
		call := c.pending[f.id.text]
		if call == nil || call.response != nil || (call.job.stage != writing && call.job.stage != written) {
			return ErrProtocol
		}
		if err := call.job.ctx.Err(); err != nil {
			return err
		}
		value := outcome{result: f.result}
		if f.err != nil {
			value.err = f.err
		}
		// At most one bounded frame is retained per admitted pending Call.
		// Receipt alone does not prove its request was completely written.
		call.response = &value
		if call.job.stage == written {
			c.finishLocked(call.job, value, finished)
		}
		return nil
	}
	var event Event
	if f.kind == requestFrame {
		if _, exists := c.consumed[f.id]; exists {
			return ErrProtocol
		}
		if c.remote[f.id] != nil {
			return ErrProtocol
		}
		if len(c.consumed) >= c.limits.ConsumedRequests {
			return ErrConsumedLimit
		}
		if len(c.remote) >= c.limits.RemoteRequests {
			return ErrRemoteLimit
		}
		request := &Request{owner: c, id: f.id, method: f.method, params: f.params}
		c.remote[f.id] = request
		event.Request = request
	} else {
		event.Notification = &Notification{Method: f.method, Params: f.params}
	}
	select {
	case c.events <- event:
		return nil
	default:
		return ErrEventLimit
	}
}
