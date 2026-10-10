package gitfilter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var ErrFilterProcessProtocol = errors.New("filter process protocol")

// ProcessExchange must invoke produce synchronously, at most once. The callback
// receives a stream, not a second in-memory copy of the Broker's input slot.
type ProcessExchange func(context.Context, uint64, string, string, func(io.Writer) error) ([]byte, error)

const (
	filterProcessPayload = 65516
	filterProcessBytes   = 16 << 20
	filterProcessPackets = 65536
	filterProcessCalls   = 1024
)

// RunFilterProcess serves one private Git filter.process v2 helper lifetime.
// A finite deadline is required. The carrier must make blocking input/output
// interruptible on cancellation; arbitrary io.Reader/Writer cannot be cancelled
// here. The carrier must also retain terminal failure across helper restarts.
func RunFilterProcess(ctx context.Context, input io.Reader, output io.Writer, exchange ProcessExchange) error {
	if ctx == nil || input == nil || output == nil || exchange == nil {
		return ErrFilterProcessProtocol
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrFilterProcessProtocol
	}
	wire := filterProcessWire{ctx: ctx, input: input, output: output}
	caps, err := wire.handshake()
	if err != nil {
		return err
	}
	for seq := uint64(1); ; seq++ {
		lines, eof, err := wire.headers(true)
		if err != nil || eof {
			return err
		}
		if seq > filterProcessCalls {
			return ErrFilterProcessProtocol
		}
		op, path, err := filterProcessRequest(lines, caps)
		if err != nil {
			return err
		}
		body := filterProcessBody{wire: &wire}
		out, exchangeErr := exchange(ctx, seq, path, op, body.produce)
		// Revocation also protects against a retained callback consuming the next
		// request. The trusted exchange contract requires synchronous production.
		body.mu.Lock()
		body.retired = true
		err = errors.Join(exchangeErr, body.produceErr, ctx.Err())
		if !body.used || !body.done || len(out) > filterProcessBytes {
			err = errors.Join(err, ErrFilterProcessProtocol)
		}
		if err != nil {
			// Never send even an abort before the content flush. The drain shares
			// the original byte/packet budgets; framing/limit failures are fatal.
			drainErr := body.consume(nil)
			body.mu.Unlock()
			if drainErr != nil {
				return errors.Join(ErrFilterProcessProtocol, err, drainErr)
			}
			return errors.Join(ErrFilterProcessProtocol, err, wire.status("abort"))
		}
		body.mu.Unlock()
		if err = wire.status("success"); err != nil {
			return err
		}
		for len(out) > 0 {
			n := min(len(out), filterProcessPayload)
			if err = wire.packet(out[:n]); err != nil {
				return err
			}
			out = out[n:]
		}
		if err = wire.flush(); err != nil {
			return err
		}
		if err = wire.flush(); err != nil { // Empty final status list.
			return err
		}
	}
}

type filterProcessWire struct {
	ctx    context.Context
	input  io.Reader
	output io.Writer
}

func (w *filterProcessWire) readFull(p []byte) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	_, err := io.ReadFull(filterProcessReader{ctx: w.ctx, input: w.input}, p)
	return err
}

type filterProcessReader struct {
	ctx   context.Context
	input io.Reader
}

func (r filterProcessReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}

func (w *filterProcessWire) readPacket() ([]byte, bool, error) {
	var header [4]byte
	if err := w.readFull(header[:]); err != nil {
		return nil, false, err
	}
	n := 0
	for _, c := range header {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		default:
			return nil, false, ErrFilterProcessProtocol
		}
	}
	if n == 0 {
		return nil, true, nil
	}
	if n < 4 || n > filterProcessPayload+4 {
		return nil, false, ErrFilterProcessProtocol
	}
	p := make([]byte, n-4)
	if err := w.readFull(p); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, false, err
	}
	return p, false, nil
}

func (w *filterProcessWire) write(p []byte) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	n, err := w.output.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *filterProcessWire) packet(p []byte) error {
	if len(p) > filterProcessPayload {
		return ErrFilterProcessProtocol
	}
	if err := w.write([]byte(fmt.Sprintf("%04x", len(p)+4))); err != nil {
		return err
	}
	return w.write(p)
}

func (w *filterProcessWire) flush() error { return w.write([]byte("0000")) }

func (w *filterProcessWire) status(s string) error {
	if err := w.packet([]byte("status=" + s + "\n")); err != nil {
		return err
	}
	return w.flush()
}

func (w *filterProcessWire) headers(eofOK bool) ([]string, bool, error) {
	var lines []string
	total := 0
	for {
		p, flush, err := w.readPacket()
		if err == io.EOF && eofOK && len(lines) == 0 {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
		if flush {
			return lines, false, nil
		}
		total += len(p)
		s := strings.TrimSuffix(string(p), "\n")
		if len(lines) >= 16 || total > 8192 || len(p) > 4096 || !filterProcessText(s, 4096) {
			return nil, false, ErrFilterProcessProtocol
		}
		lines = append(lines, s)
	}
}

func (w *filterProcessWire) handshake() (map[string]bool, error) {
	lines, _, err := w.headers(false)
	if err != nil {
		return nil, err
	}
	if len(lines) < 2 || lines[0] != "git-filter-client" {
		return nil, ErrFilterProcessProtocol
	}
	versions := make(map[string]bool)
	for _, line := range lines[1:] {
		k, v, ok := strings.Cut(line, "=")
		n, parseErr := strconv.ParseUint(v, 10, 32)
		if !ok || k != "version" || parseErr != nil || n == 0 || strconv.FormatUint(n, 10) != v || versions[v] {
			return nil, ErrFilterProcessProtocol
		}
		versions[v] = true
	}
	if !versions["2"] {
		return nil, ErrFilterProcessProtocol
	}
	// Use direct writes: Git waits for this reply before sending capabilities.
	for _, line := range []string{"git-filter-server\n", "version=2\n"} {
		if err = w.packet([]byte(line)); err != nil {
			return nil, err
		}
	}
	if err = w.flush(); err != nil {
		return nil, err
	}
	lines, _, err = w.headers(false)
	if err != nil {
		return nil, err
	}
	seen, selected := make(map[string]bool), make(map[string]bool)
	for _, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok || k != "capability" || !filterProcessNonControl(v, 4096) || seen[v] {
			return nil, ErrFilterProcessProtocol
		}
		seen[v] = true
		if v == "clean" || v == "smudge" {
			selected[v] = true
		}
	}
	if len(selected) == 0 {
		return nil, ErrFilterProcessProtocol
	}
	for _, op := range []string{"clean", "smudge"} {
		if selected[op] {
			if err = w.packet([]byte("capability=" + op + "\n")); err != nil {
				return nil, err
			}
		}
	}
	return selected, w.flush()
}

func filterProcessRequest(lines []string, caps map[string]bool) (string, string, error) {
	h := make(map[string]string)
	for _, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if _, duplicate := h[k]; !ok || duplicate {
			return "", "", ErrFilterProcessProtocol
		}
		switch k {
		case "command":
			ok = caps[v]
		case "pathname":
			ok = filterProcessText(v, 512)
		case "ref":
			ok = filterProcessNonControl(v, 1024)
		case "treeish", "blob":
			ok = len(v) == 40 || len(v) == 64
			for _, c := range v {
				ok = ok && (c >= '0' && c <= '9' || c >= 'a' && c <= 'f')
			}
		default:
			ok = false
		}
		if !ok {
			return "", "", ErrFilterProcessProtocol
		}
		h[k] = v
	}
	if h["command"] == "" || h["pathname"] == "" {
		return "", "", ErrFilterProcessProtocol
	}
	return h["command"], h["pathname"], nil
}

func filterProcessText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func filterProcessNonControl(s string, limit int) bool {
	return filterProcessText(s, limit) && strings.IndexFunc(s, unicode.IsControl) < 0
}

type filterProcessBody struct {
	mu                     sync.Mutex
	wire                   *filterProcessWire
	bytes, packets         int
	used, done, retired    bool
	produceErr, framingErr error
}

func (b *filterProcessBody) produce(dst io.Writer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.retired || b.used || dst == nil {
		b.produceErr = errors.Join(b.produceErr, ErrFilterProcessProtocol)
		return b.produceErr
	}
	b.used = true
	b.produceErr = b.consume(dst)
	return b.produceErr
}

// consume(nil) drains without retaining another payload buffer. This method is
// called with mu held and never resets the budgets after a producer failure.
func (b *filterProcessBody) consume(dst io.Writer) error {
	if b.framingErr != nil {
		return b.framingErr
	}
	for !b.done {
		p, flush, err := b.wire.readPacket()
		if err != nil {
			b.framingErr = err
			return err
		}
		if flush {
			b.done = true
			return nil
		}
		b.bytes += len(p)
		b.packets++
		if b.bytes > filterProcessBytes || b.packets > filterProcessPackets {
			b.framingErr = ErrFilterProcessProtocol
			return b.framingErr
		}
		if dst != nil {
			if err = b.wire.ctx.Err(); err != nil {
				return err
			}
			n, err := dst.Write(p)
			if err == nil && n != len(p) {
				err = io.ErrShortWrite
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
