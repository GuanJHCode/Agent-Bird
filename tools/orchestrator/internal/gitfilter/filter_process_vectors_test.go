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

func TestRunFilterProcessVectors(t *testing.T) {
	greeting := fpkt("git-filter-client") + fpkt("version=1") + fpkt("version=2") + "0000"
	caps := fpkt("capability=clean") + fpkt("capability=smudge") + fpkt("capability=delay") + "0000"
	header := fpkt("command=smudge") + fpkt("pathname=a=b") + fpkt("ref=refs/heads/a=b") + fpkt("treeish="+strings.Repeat("a", 40)) + fpkt("blob="+strings.Repeat("b", 64)) + "0000"
	t.Run("negotiated_smudge_optional_metadata_empty_body_and_revoked_producer", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var out bytes.Buffer
		reader := &fpTrackingReader{Reader: bytes.NewReader([]byte(greeting + caps + header + "00040000"))}
		var retained func(io.Writer) error
		err := RunFilterProcess(ctx, reader, &out, func(_ context.Context, n uint64, path, op string, p func(io.Writer) error) ([]byte, error) {
			if n != 1 || path != "a=b" || op != "smudge" {
				t.Fatal("request metadata not preserved")
			}
			retained = p
			return nil, p(io.Discard)
		})
		if err != nil || retained == nil {
			t.Fatal(err)
		}
		reads := reader.reads
		if err = retained(io.Discard); err == nil || reader.reads != reads {
			t.Fatal("retained producer was not revoked without IO")
		}
		want := fpkt("git-filter-server\n") + fpkt("version=2\n") + "0000" + fpkt("capability=clean\n") + fpkt("capability=smudge\n") + "0000" + fpkt("status=success\n") + "000000000000"
		if out.String() != want {
			t.Fatalf("empty response/negotiation = %q", out.String())
		}
	})
	for _, tc := range []struct{ name, input string }{
		{"hello_duplicate_version", fpkt("git-filter-client") + fpkt("version=2") + fpkt("version=2") + "0000"},
		{"hello_wrong_order", fpkt("version=2") + fpkt("git-filter-client") + "0000"},
		{"hello_unknown_key", fpkt("git-filter-client") + fpkt("version=2") + fpkt("x=y") + "0000"},
		{"hello_duplicate_greeting", fpkt("git-filter-client") + fpkt("version=2") + fpkt("git-filter-client") + "0000"},
		{"caps_duplicate", greeting + fpkt("capability=clean") + fpkt("capability=clean") + "0000"},
		{"caps_only_delay", greeting + fpkt("capability=delay") + "0000"},
		{"header_overlong_line", fpGreeting() + fpkt("pathname="+strings.Repeat("a", 4096)) + "0000"},
		{"header_list_limit", fpGreeting() + strings.Repeat(fpkt("x=y"), 17) + "0000"},
		{"header_byte_limit", fpGreeting() + strings.Repeat(fpkt("x="+strings.Repeat("a", 4093)), 3) + "0000"},
		{"header_invalid_utf8", fpGreeting() + fpkt("pathname=\xff") + "0000"},
		{"header_embedded_lf", fpGreeting() + fpkt("pathname=a\nb") + "0000"},
		{"header_unknown", fpGreeting() + fpkt("command=clean") + fpkt("pathname=a") + fpkt("can-delay=1") + "0000"},
		{"header_truncated_payload_eof", fpGreeting() + "0005"},
		{"header_truncated_length", fpGreeting() + "000"},
		{"packet_delimiter", fpGreeting() + "0001"},
		{"packet_response_end", fpGreeting() + "0002"},
		{"packet_invalid_reserved", fpGreeting() + "0003"},
		{"packet_upper_hex", fpGreeting() + "000Axxxxxx"},
		{"packet_signed_hex", fpGreeting() + "+005x"},
		{"packet_oversize", fpGreeting() + "fff1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out bytes.Buffer
			calls := 0
			err := RunFilterProcess(ctx, strings.NewReader(tc.input), &out, func(context.Context, uint64, string, string, func(io.Writer) error) ([]byte, error) {
				calls++
				return nil, nil
			})
			if err == nil || calls != 0 || strings.Contains(out.String(), "status=") {
				t.Fatalf("malformed metadata accepted or answered: error=%v calls=%d", err, calls)
			}
		})
	}
	for _, kind := range []string{"rejected_before_produce", "ignored_produce", "oversize_output", "short_producer_then_drain"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var out bytes.Buffer
			input := strings.NewReader(fpGreeting() + fpRequest("B") + fpRequest("next"))
			calls := 0
			err := RunFilterProcess(ctx, input, &out, func(_ context.Context, _ uint64, _, _ string, p func(io.Writer) error) ([]byte, error) {
				calls++
				switch kind {
				case "rejected_before_produce":
					return nil, errors.New("no permit")
				case "ignored_produce":
					return nil, nil
				case "oversize_output":
					if err := p(io.Discard); err != nil {
						t.Fatal(err)
					}
					return make([]byte, (16<<20)+1), nil
				default:
					_ = p(fpShortWriter{})
					return nil, nil
				}
			})
			if err == nil || calls != 1 || input.Len() != len(fpRequest("next")) {
				t.Fatalf("not bounded to current request: error=%v calls=%d remaining=%d", err, calls, input.Len())
			}
			if strings.Contains(out.String(), "status=success") || !strings.HasSuffix(out.String(), fpkt("status=abort\n")+"0000") {
				t.Fatal("expected only one terminal abort after drain")
			}
		})
	}
	t.Run("short_output_write", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := RunFilterProcess(ctx, strings.NewReader(fpGreeting()), fpShortWriter{}, fpConsume); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short output not detected: %v", err)
		}
	})
}
