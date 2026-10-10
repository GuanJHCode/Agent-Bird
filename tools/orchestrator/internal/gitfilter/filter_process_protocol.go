package gitfilter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrFilterProcessProtocol = errors.New("filter process protocol")

type ProcessExchange func(context.Context, uint64, string, string, func(io.Writer) error) ([]byte, error)

const fpMax = 65520

func RunFilterProcess(ctx context.Context, input io.Reader, output io.Writer, x ProcessExchange) error {
	if ctx == nil || x == nil {
		return ErrFilterProcessProtocol
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrFilterProcessProtocol
	}
	r := bufio.NewReader(input)
	w := bufio.NewWriter(output)
	if e := hello(r, w); e != nil {
		return e
	}
	var seq uint64
	for {
		h, eof, e := headers(r, true)
		if e != nil {
			return e
		}
		if eof {
			return nil
		}
		seq++
		c, p, e := request(h)
		if e != nil {
			return e
		}
		used, done := false, false
		produce := func(dst io.Writer) error {
			if used {
				return ErrFilterProcessProtocol
			}
			used = true
			for n := 0; ; n++ {
				q, f, z := readpkt(r)
				if z != nil {
					return z
				}
				if f {
					done = true
					return nil
				}
				if n >= 65536 {
					return ErrFilterProcessProtocol
				}
				if _, z = dst.Write(q); z != nil {
					return z
				}
			}
		}
		out, e := x(ctx, seq, p, c, produce)
		if !used || !done || e != nil || ctx.Err() != nil {
			_ = drain(r, done)
			_ = status(w, "abort")
			return errors.Join(ErrFilterProcessProtocol, e, ctx.Err())
		}
		if len(out) > 16<<20 {
			return ErrFilterProcessProtocol
		}
		if e = status(w, "success"); e != nil {
			return e
		}
		if e = writepkt(w, out); e != nil {
			return e
		}
		if e = flush(w); e != nil {
			return e
		}
		if e = flush(w); e != nil {
			return e
		}
		if e = w.Flush(); e != nil {
			return e
		}
	}
}
func hello(r *bufio.Reader, w *bufio.Writer) error {
	h, _, e := headers(r, false)
	if e != nil || len(h["git-filter-client"]) != 1 {
		return ErrFilterProcessProtocol
	}
	ok := false
	for _, v := range h["version"] {
		if v == "2" {
			ok = true
		}
	}
	if !ok {
		return ErrFilterProcessProtocol
	}
	if e = line(w, "git-filter-server\n"); e != nil {
		return e
	}
	if e = line(w, "version=2\n"); e != nil {
		return e
	}
	if e = flush(w); e != nil {
		return e
	}
	h, _, e = headers(r, false)
	if e != nil {
		return e
	}
	var a []string
	for _, v := range h["capability"] {
		if v == "clean" || v == "smudge" {
			a = append(a, v)
		}
	}
	if len(a) == 0 {
		return ErrFilterProcessProtocol
	}
	for _, v := range a {
		if e = line(w, "capability="+v+"\n"); e != nil {
			return e
		}
	}
	if e = flush(w); e != nil {
		return e
	}
	return w.Flush()
}
func headers(r *bufio.Reader, eofok bool) (map[string][]string, bool, error) {
	h := map[string][]string{}
	n, b := 0, 0
	for {
		p, f, e := readpkt(r)
		if e == io.EOF && eofok && n == 0 {
			return h, true, nil
		}
		if e != nil {
			return nil, false, e
		}
		if f {
			return h, false, nil
		}
		n++
		b += len(p)
		s := strings.TrimSuffix(string(p), "\n")
		i := strings.IndexByte(s, '=')
		if n > 16 || b > 8192 || len(p) > 4096 || i < 0 {
			if s == "git-filter-client" && i < 0 && len(h[s]) == 0 {
				h[s] = []string{""}
				continue
			}
			return nil, false, ErrFilterProcessProtocol
		}
		k, v := s[:i], s[i+1:]
		if k == "version" || k == "capability" {
			h[k] = append(h[k], v)
			continue
		}
		if k == "" || len(h[k]) > 0 {
			return nil, false, ErrFilterProcessProtocol
		}
		h[k] = []string{v}
	}
}
func request(h map[string][]string) (string, string, error) {
	if len(h["command"]) != 1 || len(h["pathname"]) != 1 {
		return "", "", ErrFilterProcessProtocol
	}
	c, p := h["command"][0], h["pathname"][0]
	if (c != "clean" && c != "smudge") || !text(p, 512) {
		return "", "", ErrFilterProcessProtocol
	}
	for k, v := range h {
		if k == "command" || k == "pathname" {
			continue
		}
		if k == "ref" && text(v[0], 1024) {
			continue
		}
		if (k == "treeish" || k == "blob") && (len(v[0]) == 40 || len(v[0]) == 64) {
			ok := true
			for _, z := range v[0] {
				ok = ok && (z >= '0' && z <= '9' || z >= 'a' && z <= 'f')
			}
			if ok {
				continue
			}
		}
		return "", "", ErrFilterProcessProtocol
	}
	return c, p, nil
}
func text(s string, n int) bool {
	return s != "" && len(s) <= n && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func readpkt(r *bufio.Reader) ([]byte, bool, error) {
	h := make([]byte, 4)
	if _, e := io.ReadFull(r, h); e != nil {
		return nil, false, e
	}
	n, e := strconv.ParseUint(string(h), 16, 16)
	if e != nil || n > fpMax || n == 1 || n == 2 || n == 3 {
		return nil, false, ErrFilterProcessProtocol
	}
	if n == 0 {
		return nil, true, nil
	}
	if n < 4 {
		return nil, false, ErrFilterProcessProtocol
	}
	p := make([]byte, n-4)
	_, e = io.ReadFull(r, p)
	return p, false, e
}
func line(w *bufio.Writer, s string) error { return writepkt(w, []byte(s)) }
func writepkt(w *bufio.Writer, p []byte) error {
	if len(p) > 65516 {
		return ErrFilterProcessProtocol
	}
	_, e := fmt.Fprintf(w, "%04x", len(p)+4)
	if e == nil {
		_, e = w.Write(p)
	}
	return e
}
func flush(w *bufio.Writer) error { _, e := w.WriteString("0000"); return e }
func status(w *bufio.Writer, s string) error {
	if e := line(w, "status="+s+"\n"); e != nil {
		return e
	}
	if e := flush(w); e != nil {
		return e
	}
	return w.Flush()
}
func drain(r *bufio.Reader, d bool) error {
	if d {
		return nil
	}
	for {
		_, f, e := readpkt(r)
		if e != nil {
			return e
		}
		if f {
			return nil
		}
	}
}
