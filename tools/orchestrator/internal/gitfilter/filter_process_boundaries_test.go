package gitfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func fpGreeting() string {
	return fpkt("git-filter-client\n") + fpkt("version=2\n") + "0000" + fpkt("capability=clean\n") + "0000"
}
func fpRequest(body string) string {
	return fpkt("command=clean\n") + fpkt("pathname=a\n") + "0000" + fpkt(body) + "0000"
}
func fpConsume(_ context.Context, _ uint64, _, _ string, produce func(io.Writer) error) ([]byte, error) {
	return nil, produce(io.Discard)
}

type fpShortWriter struct{}

func (fpShortWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		return len(p) - 1, nil
	}
	return 0, nil
}

type fpTrackingReader struct {
	*bytes.Reader
	reads int
}

func (r *fpTrackingReader) Read(p []byte) (int, error) { r.reads++; return r.Reader.Read(p) }

func TestRunFilterProcessBoundaries(t *testing.T) {
	t.Run("interactive_version_reply_before_capabilities", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		input, send := io.Pipe()
		receive, output := io.Pipe()
		defer input.Close()
		defer send.Close()
		defer receive.Close()
		defer output.Close()
		stop := context.AfterFunc(ctx, func() {
			input.CloseWithError(ctx.Err())
			send.CloseWithError(ctx.Err())
			receive.CloseWithError(ctx.Err())
			output.CloseWithError(ctx.Err())
		})
		defer stop()
		server := make(chan error, 1)
		go func() { server <- RunFilterProcess(ctx, input, output, fpConsume) }()
		if _, err := io.WriteString(send, fpkt("git-filter-client\n")+fpkt("version=2\n")+"0000"); err != nil {
			t.Fatal(err)
		}
		want := fpkt("git-filter-server\n") + fpkt("version=2\n") + "0000"
		got := make([]byte, len(want))
		_, err := io.ReadFull(receive, got)
		if err != nil {
			cancel()
			<-server
			t.Fatalf("version response blocked before Git sends capabilities: %v", err)
		}
		if string(got) != want {
			t.Fatalf("hello=%q", got)
		}
		if _, err = io.WriteString(send, fpkt("capability=clean\n")+"0000"); err != nil {
			t.Fatal(err)
		}
		got = make([]byte, len(fpkt("capability=clean\n")+"0000"))
		if _, err = io.ReadFull(receive, got); err != nil {
			t.Fatal(err)
		}
		send.Close()
		if err = <-server; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("large_output_split_into_packets", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var output bytes.Buffer
		payload := bytes.Repeat([]byte{'x'}, 65517)
		err := RunFilterProcess(ctx, strings.NewReader(fpGreeting()+fpRequest("B")), &output, func(_ context.Context, _ uint64, _, _ string, p func(io.Writer) error) ([]byte, error) {
			if e := p(io.Discard); e != nil {
				return nil, e
			}
			return payload, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := fpkt("git-filter-server\n") + fpkt("version=2\n") + "0000" + fpkt("capability=clean\n") + "0000" + fpkt("status=success\n") + "0000" + fpkt(string(payload[:65516])) + fpkt(string(payload[65516:])) + "00000000"
		if output.String() != want {
			t.Fatal("response packetization mismatch")
		}
	})
	for _, kind := range []string{"unnegotiated_op", "cancelled", "produce_twice_ignored", "short_producer_write_ignored", "body_byte_limit", "request_limit", "truncated_reject_no_abort", "header_duplicate", "metadata_invalid", "packet_empty_limit"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			request := fpRequest("B")
			exchange := ProcessExchange(fpConsume)
			calls := 0
			switch kind {
			case "unnegotiated_op":
				request = fpkt("command=smudge\n") + fpkt("pathname=a\n") + "0000" + fpkt("B") + "0000"
			case "cancelled":
				cancel()
			case "produce_twice_ignored":
				exchange = func(_ context.Context, _ uint64, _, _ string, p func(io.Writer) error) ([]byte, error) {
					_ = p(io.Discard)
					_ = p(io.Discard)
					return []byte("unsafe"), nil
				}
			case "short_producer_write_ignored":
				exchange = func(_ context.Context, _ uint64, _, _ string, p func(io.Writer) error) ([]byte, error) {
					_ = p(fpShortWriter{})
					return []byte("unsafe"), nil
				}
			case "body_byte_limit":
				request = fpkt("command=clean\n") + fpkt("pathname=a\n") + "0000" + strings.Repeat(fpkt(strings.Repeat("x", 65516)), 257) + "0000"
			case "request_limit":
				request = strings.Repeat(fpRequest(""), 1025)
			case "truncated_reject_no_abort":
				request = fpkt("command=clean\n") + fpkt("pathname=a\n") + "0000" + "0006x"
				exchange = func(context.Context, uint64, string, string, func(io.Writer) error) ([]byte, error) {
					return nil, errors.New("reject before body")
				}
			case "header_duplicate":
				request = fpkt("command=clean\n") + fpkt("pathname=a\n") + fpkt("version=2\n") + fpkt("version=2\n") + "0000" + "0000"
			case "metadata_invalid":
				request = fpkt("command=clean\n") + fpkt("pathname=a\n") + fpkt("ref=refs/heads/a\tbad\n") + "0000" + "0000"
			case "packet_empty_limit":
				request = fpkt("command=clean\n") + fpkt("pathname=a\n") + "0000" + strings.Repeat("0004", 65537) + "0000"
			}
			reader := &fpTrackingReader{Reader: bytes.NewReader([]byte(fpGreeting() + request))}
			var output bytes.Buffer
			err := RunFilterProcess(ctx, reader, &output, func(c context.Context, n uint64, p, o string, produce func(io.Writer) error) ([]byte, error) {
				calls++
				return exchange(c, n, p, o, produce)
			})
			if err == nil {
				t.Fatal("invalid request accepted")
			}
			if kind == "cancelled" && (reader.reads != 0 || output.Len() != 0) {
				t.Fatal("cancelled codec did IO")
			}
			if kind == "request_limit" && calls != 1024 {
				t.Fatalf("calls=%d", calls)
			}
			if kind == "unnegotiated_op" && calls != 0 {
				t.Fatal("unnegotiated operation reached callback")
			}
			if kind == "truncated_reject_no_abort" && strings.Contains(output.String(), "status=") {
				t.Fatal("response before body flush")
			}
			if kind != "request_limit" && strings.Contains(output.String(), "status=success") {
				t.Fatal("failure published success")
			}
		})
	}
}
