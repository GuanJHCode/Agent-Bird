// resource-synthetic-worker is an unauthenticated provider-shaped load fixture.
// Its sidecars prove what the fixture emitted; they are never product evidence.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	version      = "resource-synthetic-worker/1"
	configPrefix = "RESOURCE_CONFIG_V1:"
)

type config struct {
	Scenario                  string `json:"scenario"`
	Mode                      string `json:"mode"`
	WorkerID                  string `json:"worker_id"`
	DurationSeconds           int    `json:"duration_seconds"`
	BytesPerSecond            int    `json:"bytes_per_second"`
	GiantLineBytes            int    `json:"giant_line_bytes"`
	GiantLineIntervalSeconds  int    `json:"giant_line_interval_seconds"`
	ReportIntervalSeconds     int    `json:"report_interval_seconds"`
	ReportMaximumBytes        int    `json:"report_maximum_bytes"`
	StatsPath                 string `json:"stats_path"`
	EmissionReportSidecarPath string `json:"emission_report_sidecar_path"`
}

type stats struct {
	SchemaVersion           int    `json:"schema_version"`
	Scenario                string `json:"scenario"`
	Mode                    string `json:"mode"`
	WorkerID                string `json:"worker_id"`
	PID                     int    `json:"pid"`
	StartedUnixNano         int64  `json:"started_unix_nano"`
	TerminalEmittedUnixNano int64  `json:"terminal_emitted_unix_nano,omitempty"`
	CompletedUnixNano       int64  `json:"completed_unix_nano,omitempty"`
	StdoutBytes             int64  `json:"stdout_bytes"`
	StderrBytes             int64  `json:"stderr_bytes"`
	GiantLines              int64  `json:"giant_lines"`
	EmissionReports         int64  `json:"emission_reports"`
	ProductReportAttempts   int64  `json:"product_report_attempts"`
	ProductReportSuccesses  int64  `json:"product_report_successes"`
	ProductReportCPUNanos   int64  `json:"product_report_cpu_nanos"`
	ProductReportMaxRSS     int64  `json:"product_report_max_rss_bytes"`
	TerminalWireSHA256      string `json:"terminal_wire_sha256,omitempty"`
	TerminalSentinel        string `json:"terminal_sentinel,omitempty"`
	Status                  string `json:"status"`
	Error                   string `json:"error,omitempty"`
}

type counters struct {
	stdout, stderr, giants, reports, reportSequence, reportAttempts, reportSuccesses atomic.Int64
	reportCPUNanos, reportMaxRSS                                                     atomic.Int64
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			fmt.Println(version)
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "resource-synthetic-worker:", err)
		os.Exit(2)
	}
}

func run() error {
	cfg, err := readConfig(os.Stdin)
	if err != nil {
		return err
	}
	if err = validateConfig(cfg); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	started := time.Now()
	current := stats{SchemaVersion: 1, Scenario: cfg.Scenario, Mode: cfg.Mode, WorkerID: cfg.WorkerID, PID: os.Getpid(), StartedUnixNano: started.UnixNano(), Status: "running"}
	if err = writeStats(cfg.StatsPath, current); err != nil {
		return err
	}
	reportFile, err := os.OpenFile(cfg.EmissionReportSidecarPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer reportFile.Close()

	var count counters
	var reporters sync.WaitGroup
	reportCtx, reportCancel := context.WithCancel(ctx)
	if cfg.ReportIntervalSeconds > 0 {
		reporters.Add(1)
		go func() {
			defer reporters.Done()
			reportLoop(reportCtx, cfg, reportFile, &count, started, started.Add(time.Duration(cfg.DurationSeconds)*time.Second))
		}()
	}

	terminal := "RESOURCE_TERMINAL:" + cfg.Scenario + ":" + cfg.WorkerID
	var terminalWire []byte
	var terminalEmittedUnixNano int64
	if cfg.Mode == "oneshot" {
		terminalWire = claudeResult(cfg.WorkerID, terminal)
		terminalEmittedUnixNano = time.Now().UnixNano()
		if _, err = os.Stdout.Write(terminalWire); err == nil {
			count.stdout.Add(int64(len(terminalWire)))
		} else {
			terminalEmittedUnixNano = 0
		}
	} else if cfg.Mode == "steady" {
		err = waitUntil(ctx, started.Add(time.Duration(cfg.DurationSeconds)*time.Second))
		terminalWire = claudeResult(cfg.WorkerID, terminal)
		if err == nil {
			terminalEmittedUnixNano = time.Now().UnixNano()
			if _, writeErr := os.Stdout.Write(terminalWire); writeErr != nil {
				err = writeErr
				terminalEmittedUnixNano = 0
			} else {
				count.stdout.Add(int64(len(terminalWire)))
			}
		}
	} else {
		err = emitBurst(ctx, cfg, &count, started)
		terminalWire = claudeResult(cfg.WorkerID, terminal)
		if err == nil {
			terminalEmittedUnixNano = time.Now().UnixNano()
			if _, writeErr := os.Stdout.Write(terminalWire); writeErr != nil {
				err = writeErr
				terminalEmittedUnixNano = 0
			} else {
				count.stdout.Add(int64(len(terminalWire)))
			}
		}
	}
	if terminalEmittedUnixNano > 0 {
		current.TerminalEmittedUnixNano = terminalEmittedUnixNano
		current.StdoutBytes = count.stdout.Load()
		current.StderrBytes = count.stderr.Load()
		current.GiantLines = count.giants.Load()
		current.TerminalSentinel = terminal
		current.TerminalWireSHA256 = sha256Hex(terminalWire)
		current.Status = "terminal_emitted"
		if statsErr := writeStats(cfg.StatsPath, current); statsErr != nil && err == nil {
			err = statsErr
		}
	}
	reportCancel()
	reporters.Wait()
	if len(terminalWire) > 0 {
		sequence := int(count.reportSequence.Add(1))
		eventID := fmt.Sprintf("resource-%s-%06d", cfg.WorkerID, sequence)
		terminalPayload, payloadErr := terminalReportPayload(cfg, terminal)
		if payloadErr != nil && err == nil {
			err = payloadErr
		}
		_ = writeEmissionReport(reportFile, cfg, &count, time.Now(), "result", eventID, sequence, terminalPayload)
		terminalCtx, terminalCancel := context.WithTimeout(context.Background(), 2*time.Second)
		tryProductReport(terminalCtx, cfg, &count, eventID, sequence, "result", terminalPayload)
		terminalCancel()
	}
	_ = reportFile.Sync()

	current.CompletedUnixNano = time.Now().UnixNano()
	current.TerminalEmittedUnixNano = terminalEmittedUnixNano
	current.StdoutBytes = count.stdout.Load()
	current.StderrBytes = count.stderr.Load()
	current.GiantLines = count.giants.Load()
	current.EmissionReports = count.reports.Load()
	current.ProductReportAttempts = count.reportAttempts.Load()
	current.ProductReportSuccesses = count.reportSuccesses.Load()
	current.ProductReportCPUNanos = count.reportCPUNanos.Load()
	current.ProductReportMaxRSS = count.reportMaxRSS.Load()
	current.TerminalSentinel = terminal
	current.TerminalWireSHA256 = sha256Hex(terminalWire)
	current.Status = "completed"
	if err != nil {
		current.Status = "failed"
		current.Error = err.Error()
	}
	if statsErr := writeStats(cfg.StatsPath, current); err == nil {
		err = statsErr
	}
	return err
}

func readConfig(reader io.Reader) (config, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 128*1024))
	if err != nil {
		return config{}, err
	}
	var envelope struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	var decodedConfig config
	if json.Unmarshal(data, &envelope) != nil {
		return config{}, errors.New("provider_input_invalid")
	}
	for _, item := range envelope.Message.Content {
		if strings.HasPrefix(item.Text, configPrefix) {
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(item.Text, configPrefix))
			if decodeErr != nil || json.Unmarshal(decoded, &decodedConfig) != nil {
				return config{}, errors.New("resource_config_invalid")
			}
			return decodedConfig, nil
		}
	}
	return config{}, errors.New("resource_config_missing")
}

func validateConfig(cfg config) error {
	if cfg.Scenario == "" || cfg.WorkerID == "" || cfg.DurationSeconds < 0 {
		return errors.New("resource_config_invalid")
	}
	if cfg.Mode != "oneshot" && cfg.Mode != "steady" && cfg.Mode != "burst-valid" && cfg.Mode != "burst-unknown" {
		return errors.New("resource_mode_invalid")
	}
	for _, path := range []string{cfg.StatsPath, cfg.EmissionReportSidecarPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("resource_sidecar_path_invalid")
		}
	}
	if strings.HasPrefix(cfg.Mode, "burst-") && (cfg.BytesPerSecond <= 0 || cfg.GiantLineBytes <= 0 || cfg.GiantLineIntervalSeconds <= 0 || cfg.ReportIntervalSeconds <= 0 || cfg.ReportMaximumBytes <= 0 || cfg.ReportMaximumBytes > 4096) {
		return errors.New("resource_burst_config_invalid")
	}
	return nil
}

func emitBurst(ctx context.Context, cfg config, count *counters, started time.Time) error {
	const chunksPerSecond = 32
	chunkBytes := cfg.BytesPerSecond / chunksPerSecond
	if chunkBytes < 128 || chunkBytes*chunksPerSecond != cfg.BytesPerSecond {
		return errors.New("resource_rate_not_divisible")
	}
	deadline := started.Add(time.Duration(cfg.DurationSeconds) * time.Second)
	interval := time.Second / chunksPerSecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	chunkIndex := 0
	nextGiant := started.Add(time.Duration(cfg.GiantLineIntervalSeconds) * time.Second)
	emitDueGiants := func(now time.Time) error {
		for !now.Before(nextGiant) && !nextGiant.After(deadline) {
			line := giantLine(cfg.Mode, cfg.GiantLineBytes)
			if _, err := os.Stdout.Write(line); err != nil {
				return err
			}
			count.stdout.Add(int64(len(line)))
			count.giants.Add(1)
			nextGiant = nextGiant.Add(time.Duration(cfg.GiantLineIntervalSeconds) * time.Second)
		}
		return nil
	}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if err := emitDueGiants(now); err != nil {
				return err
			}
			line := outputLine(cfg.Mode, cfg.WorkerID, chunkIndex, chunkBytes)
			writer := io.Writer(os.Stdout)
			counter := &count.stdout
			if chunkIndex%4 == 3 {
				writer, counter = os.Stderr, &count.stderr
			}
			if _, err := writer.Write(line); err != nil {
				return err
			}
			counter.Add(int64(len(line)))
			chunkIndex++
		}
	}
	// The deadline itself is part of the declared 20-minute schedule. A loop
	// condition evaluated just after that instant must not randomly omit the
	// 40th giant line due at exactly +1200s.
	if err := emitDueGiants(deadline); err != nil {
		return err
	}
	return nil
}

func outputLine(mode, workerID string, sequence, size int) []byte {
	if mode == "burst-unknown" {
		prefix := fmt.Sprintf("UNKNOWN:%s:%d:", workerID, sequence)
		return paddedLine(prefix, size, 'x')
	}
	prefix := fmt.Sprintf(`{"type":"assistant","session_id":%q,"sequence":%d,"padding":"`, workerID, sequence)
	suffix := `"}` + "\n"
	return paddedDelimited(prefix, suffix, size, 'v')
}

func giantLine(mode string, size int) []byte {
	if mode == "burst-valid" {
		return paddedDelimited(`{"type":"system","subtype":"diagnostic","padding":"`, `"}`+"\n", size, 'g')
	}
	return paddedLine("UNKNOWN_GIANT:", size, 'G')
}

func paddedLine(prefix string, size int, fill byte) []byte {
	return paddedDelimited(prefix, "\n", size, fill)
}

func paddedDelimited(prefix, suffix string, size int, fill byte) []byte {
	if size < len(prefix)+len(suffix) {
		size = len(prefix) + len(suffix)
	}
	out := bytes.Repeat([]byte{fill}, size)
	copy(out, prefix)
	copy(out[size-len(suffix):], suffix)
	return out
}

func claudeResult(workerID, terminal string) []byte {
	data, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": workerID, "result": terminal})
	return append(data, '\n')
}

func reportLoop(ctx context.Context, cfg config, sidecar *os.File, count *counters, started, deadline time.Time) {
	ticker := time.NewTicker(time.Duration(cfg.ReportIntervalSeconds) * time.Second)
	defer ticker.Stop()
	sequence := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !now.Before(deadline) {
				return
			}
			sequence++
			wire, _ := json.Marshal(map[string]any{"status": "resource_sample", "sequence": sequence, "elapsed_ms": now.Sub(started).Milliseconds()})
			wire = append(wire, '\n')
			if len(wire) > cfg.ReportMaximumBytes {
				return
			}
			count.reportSequence.Store(int64(sequence))
			eventID := fmt.Sprintf("resource-%s-%06d", cfg.WorkerID, sequence)
			_ = writeEmissionReport(sidecar, cfg, count, now, "progress", eventID, sequence, wire)
			tryProductReport(ctx, cfg, count, eventID, sequence, "progress", wire)
		}
	}
}

func terminalReportPayload(cfg config, terminal string) ([]byte, error) {
	root := filepath.Dir(cfg.StatsPath)
	if capability := os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY"); capability != "" {
		root = filepath.Dir(capability)
	}
	artifactPath := filepath.Join(root, "resource-report-artifact-"+cfg.WorkerID+".txt")
	artifactData := []byte(terminal + "\n")
	file, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(artifactData); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"status": "completed", "artifact": map[string]any{"id": filepath.Base(artifactPath), "path": artifactPath, "size": len(artifactData), "sha256": sha256Hex(artifactData)}})
	return payload, err
}

func writeEmissionReport(sidecar *os.File, cfg config, count *counters, when time.Time, kind, eventID string, sequence int, wire []byte) error {
	record, _ := json.Marshal(map[string]any{"schema_version": 1, "scenario": cfg.Scenario, "worker_id": cfg.WorkerID, "event_id": eventID, "sequence": sequence, "kind": kind, "emitted_unix_nano": when.UnixNano(), "wire_bytes": len(wire), "wire_sha256": sha256Hex(wire)})
	if _, err := sidecar.Write(append(record, '\n')); err != nil {
		return err
	}
	count.reports.Add(1)
	return sidecar.Sync()
}

// The product owns these environment variables and the attempt capability.
// Their absence is recorded as a product contract gap, not silently bypassed.
func tryProductReport(ctx context.Context, cfg config, count *counters, eventID string, sequence int, kind string, wire []byte) {
	executable := os.Getenv("ORCHESTRATOR_REPORT_EXECUTABLE")
	capability := os.Getenv("ORCHESTRATOR_REPORT_CAPABILITY")
	if executable == "" || capability == "" {
		return
	}
	count.reportAttempts.Add(1)
	requestPath := filepath.Join(filepath.Dir(cfg.StatsPath), fmt.Sprintf("resource-report-request-%s-%06d.json", cfg.WorkerID, sequence))
	request, _ := json.Marshal(map[string]any{"version": 1, "event_id": eventID, "sequence": sequence, "kind": kind, "payload": json.RawMessage(bytes.TrimSpace(wire))})
	if err := os.WriteFile(requestPath, append(request, '\n'), 0600); err != nil {
		return
	}
	defer os.Remove(requestPath)
	command := exec.CommandContext(ctx, executable, "report", "--capability-file", capability, "--request", requestPath)
	command.Stdin, command.Stdout, command.Stderr = nil, io.Discard, io.Discard
	runErr := command.Run()
	if command.ProcessState != nil {
		if usage, ok := command.ProcessState.SysUsage().(*syscall.Rusage); ok {
			cpuNanos := int64(usage.Utime.Sec+usage.Stime.Sec)*int64(time.Second) + int64(usage.Utime.Usec+usage.Stime.Usec)*int64(time.Microsecond)
			count.reportCPUNanos.Add(cpuNanos)
			for current := count.reportMaxRSS.Load(); usage.Maxrss > current && !count.reportMaxRSS.CompareAndSwap(current, usage.Maxrss); current = count.reportMaxRSS.Load() {
			}
		}
	}
	if runErr == nil {
		count.reportSuccesses.Add(1)
	}
}

func waitUntil(ctx context.Context, deadline time.Time) error {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeStats(path string, value stats) error {
	data, _ := json.MarshalIndent(value, "", "  ")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
