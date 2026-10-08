package main

// Task-private load generator. The production executable never dispatches it.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type soakPhase struct {
	Seconds     int    `json:"seconds"`
	StdoutBytes int64  `json:"stdout_bytes"`
	StderrBytes int64  `json:"stderr_bytes"`
	Giants      int    `json:"giants"`
	Reports     int    `json:"reports"`
	StdoutSHA   string `json:"stdout_sha256"`
	StderrSHA   string `json:"stderr_sha256"`
}
type soakWorkerStats struct {
	PID               int          `json:"pid"`
	Status            string       `json:"status"`
	Error             string       `json:"error,omitempty"`
	Phases            [3]soakPhase `json:"phases"`
	HelperCPUNS       int64        `json:"helper_cpu_ns"`
	HelperMaxRSSBytes int64        `json:"helper_max_rss_bytes"`
	Reports           int          `json:"reports"`
}

func soakAtomicJSON(path string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path+".next", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(append(raw, '\n'))
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(path+".next", path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}

// Exactly n bytes including the final newline, without an embedded newline.
func resourceSoakLine(valid bool, n int) []byte {
	if !valid {
		return append(bytes.Repeat([]byte{'U'}, n-1), '\n')
	}
	prefix := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"`)
	suffix := []byte("\"}]}}\n")
	if n < len(prefix)+len(suffix) {
		panic("fixture line too short")
	}
	return append(append(prefix, bytes.Repeat([]byte{'R'}, n-len(prefix)-len(suffix))...), suffix...)
}

func TestResourceSoakLinesHaveExactWireShape(t *testing.T) {
	for _, valid := range []bool{true, false} {
		for _, size := range []int{2048, 4194304} {
			line := resourceSoakLine(valid, size)
			if len(line) != size || bytes.Count(line, []byte{'\n'}) != 1 || line[size-1] != '\n' {
				t.Fatal("line size/newline drift", valid, size)
			}
			if json.Valid(bytes.TrimSpace(line)) != valid {
				t.Fatal("phase schema shape drift", valid, size)
			}
		}
	}
}

func runResourceSoakWorkerFixture() {
	if e := resourceSoakWorker(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
}
func resourceSoakWorker() (retErr error) {
	fs := flag.NewFlagSet("resource-soak-worker-fixture", flag.ContinueOnError)
	id := fs.String("id", "", "")
	duration := fs.Int("seconds", 0, "")
	phaseSeconds := fs.Int("phase-seconds", 1200, "")
	rate := fs.Int("bytes-per-second", 1048576, "")
	giant := fs.Int("giant-line-bytes", 4194304, "")
	giantEvery := fs.Int("giant-line-every", 30, "")
	reportEvery := fs.Int("critical-every", 10, "")
	statsPath := fs.String("stats-file", "", "")
	startPath := fs.String("start-file", "", "")
	receiptPath := fs.String("receipt-file", "", "")
	if e := fs.Parse(os.Args[1:]); e != nil {
		return e
	}
	if *id == "" || *duration < 1 || *phaseSeconds < 1 || *rate < 4096 || *giant < 4096 || *giantEvery < 1 || *reportEvery < 1 || !filepath.IsAbs(*statsPath) || !filepath.IsAbs(*startPath) || !filepath.IsAbs(*receiptPath) {
		return errors.New("invalid resource fixture arguments")
	}
	stats := soakWorkerStats{PID: os.Getpid(), Status: "waiting"}
	stdoutHashes, stderrHashes := [3]hash.Hash{sha256.New(), sha256.New(), sha256.New()}, [3]hash.Hash{sha256.New(), sha256.New(), sha256.New()}
	save := func() error {
		for i := range stats.Phases {
			stats.Phases[i].StdoutSHA = hex.EncodeToString(stdoutHashes[i].Sum(nil))
			stats.Phases[i].StderrSHA = hex.EncodeToString(stderrHashes[i].Sum(nil))
		}
		return soakAtomicJSON(*statsPath, stats)
	}
	defer func() {
		if retErr != nil {
			stats.Status = "failed"
			stats.Error = retErr.Error()
		}
		retErr = errors.Join(retErr, save())
	}()
	if e := save(); e != nil {
		return e
	}
	receipts, e := os.OpenFile(*receiptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer receipts.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	var start struct {
		UnixNS int64 `json:"unix_ns"`
	}
	for {
		b, e := os.ReadFile(*startPath)
		if e == nil {
			if json.Unmarshal(b, &start) != nil || start.UnixNS <= 0 {
				return errors.New("start marker invalid")
			}
			break
		}
		if !os.IsNotExist(e) {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if d := time.Until(time.Unix(0, start.UnixNS)); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	started := time.Now()
	stats.Status = "running"
	var capability reportCapabilityFile
	capRaw, e := os.ReadFile(os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY"))
	if e != nil || json.Unmarshal(capRaw, &capability) != nil || capability.CapabilityID == "" {
		return errors.New("report capability absent")
	}
	for tick := 0; tick < *duration; tick++ {
		select {
		case <-ctx.Done():
			stats.Status = "stopped"
			return nil
		default:
		}
		phase := tick / *phaseSeconds
		if phase > 2 {
			phase = 2
		}
		p := &stats.Phases[phase]
		emit := func(target *os.File, h hash.Hash, n int64) error {
			for n > 0 {
				chunk := int64(4096)
				if n < chunk {
					chunk = n
				}
				line := resourceSoakLine(phase == 0, int(chunk))
				written, e := io.MultiWriter(target, h).Write(line)
				if target == os.Stdout {
					p.StdoutBytes += int64(written)
				} else {
					p.StderrBytes += int64(written)
				}
				if e != nil {
					return e
				}
				n -= int64(written)
			}
			return nil
		}
		if phase < 2 {
			if e := emit(os.Stdout, stdoutHashes[phase], int64(*rate/2)); e != nil {
				return e
			}
			if e := emit(os.Stderr, stderrHashes[phase], int64(*rate-*rate/2)); e != nil {
				return e
			}
			if (tick+1)%*giantEvery == 0 {
				line := resourceSoakLine(phase == 0, *giant)
				n, e := io.MultiWriter(os.Stdout, stdoutHashes[phase]).Write(line)
				p.StdoutBytes += int64(n)
				if e != nil {
					return e
				}
				p.Giants++
			}
		}
		if (tick+1)%*reportEvery == 0 {
			seq := stats.Reports + 1
			eventID := fmt.Sprintf("resource-%s-%d", *id, seq)
			payload, _ := json.Marshal(map[string]any{"status": "resource_sample", "sequence": seq, "elapsed_ms": time.Since(started).Milliseconds()})
			if len(payload) > 4096 {
				return errors.New("report too large")
			}
			request := filepath.Join(filepath.Dir(*statsPath), fmt.Sprintf("report-%s-%d.json", *id, seq))
			if e := soakAtomicJSON(request, reportFile{Version: 1, EventID: eventID, Sequence: int64(seq), Kind: "progress", Payload: payload}); e != nil {
				return e
			}
			op, stop := context.WithTimeout(ctx, 5*time.Second)
			cmd := exec.CommandContext(op, os.Getenv("ORCHESTRATOR_REPORT_EXECUTABLE"), "report", "--capability-file", os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY"), "--request", request)
			ack, e := cmd.Output()
			stop()
			if e != nil {
				var failure *exec.ExitError
				if errors.As(e, &failure) {
					return fmt.Errorf("report original Wait: %w; stderr: %s", e, strings.TrimSpace(string(failure.Stderr)))
				}
				return fmt.Errorf("report original Wait: %w", e)
			}
			var receipt contract.DurableAck
			if json.Unmarshal(ack, &receipt) != nil || receipt.Status != "durable" || receipt.AckedThrough != int64(seq) || receipt.EventID != eventID || receipt.ProducerID != capability.CapabilityID {
				return errors.New("report ACK not exact")
			}
			u := cmd.ProcessState.SysUsage().(*syscall.Rusage)
			cpu := int64(u.Utime.Sec+u.Stime.Sec)*int64(time.Second) + int64(u.Utime.Usec+u.Stime.Usec)*int64(time.Microsecond)
			rss := int64(u.Maxrss)
			if runtime.GOOS != "darwin" {
				rss *= 1024
			}
			stats.HelperCPUNS += cpu
			if rss > stats.HelperMaxRSSBytes {
				stats.HelperMaxRSSBytes = rss
			}
			stats.Reports++
			p.Reports++
			record := map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "phase": phase, "worker_pid": os.Getpid(), "helper_pid": cmd.Process.Pid, "original_wait": 0, "helper_cpu_ns": cpu, "helper_max_rss_bytes": rss, "request_sha256": fmt.Sprintf("%x", sha256.Sum256(payload)), "ack": receipt}
			if e = json.NewEncoder(receipts).Encode(record); e != nil {
				return e
			}
			if e = receipts.Sync(); e != nil {
				return e
			}
			if e = os.Remove(request); e != nil {
				return e
			}
		}
		p.Seconds++
		if e := save(); e != nil {
			return e
		}
		if pause := time.Until(started.Add(time.Duration(tick+1) * time.Second)); pause > 0 {
			select {
			case <-ctx.Done():
				stats.Status = "stopped"
				return nil
			case <-time.After(pause):
			}
		}
	}
	stats.Status = "completed"
	return nil
}
