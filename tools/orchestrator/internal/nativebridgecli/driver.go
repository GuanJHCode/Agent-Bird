package nativebridgecli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type driverRuntime struct {
	Version                 int               `json:"version"`
	PackageRoot             string            `json:"package_root"`
	PackageManifest         string            `json:"package_manifest"`
	PackageManifestSHA256   string            `json:"package_manifest_sha256"`
	Interpreter             string            `json:"interpreter"`
	InterpreterSHA256       string            `json:"interpreter_sha256"`
	G0RuntimeRoot           string            `json:"g0_runtime_root"`
	G0RuntimeManifest       string            `json:"g0_runtime_manifest"`
	G0RuntimeManifestSHA256 string            `json:"g0_runtime_manifest_sha256"`
	G0Modules               map[string]string `json:"g0_modules"`
}

type driverRequest struct {
	Version                 int           `json:"version"`
	Runtime                 driverRuntime `json:"runtime"`
	DriverRoot              string        `json:"driver_root"`
	ManifestPath            string        `json:"manifest_path"`
	ManifestSHA256          string        `json:"manifest_sha256"`
	ActivationID            string        `json:"activation_id"`
	OwnerReadyReceipt       string        `json:"owner_ready_receipt"`
	OwnerReadySHA256        string        `json:"owner_ready_sha256"`
	ProfileID               string        `json:"profile_id"`
	PublicSocket            string        `json:"public_socket"`
	BridgeRoot              string        `json:"bridge_root"`
	OwnerCapabilityRoot     string        `json:"owner_capability_root"`
	G1State                 string        `json:"g1_state"`
	Mode                    string        `json:"mode"`
	SubmitRequest           string        `json:"submit_request,omitempty"`
	ControlFile             string        `json:"control_file,omitempty"`
	TaskID                  string        `json:"task_id"`
	BootstrapTimeoutSeconds int           `json:"bootstrap_timeout_seconds"`
	WatchPolicy             string        `json:"watch_policy"`
	EnableTestFake          bool          `json:"enable_test_fake,omitempty"`
}

type driverLauncherReceipt struct {
	Version          int    `json:"version"`
	PID              int    `json:"pid"`
	Birth            string `json:"birth"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	RequestSHA256    string `json:"request_sha256"`
}

type DriverDecision struct {
	Decision  string `json:"decision"`
	CommandID string `json:"command_id"`
}

type decisionRequest struct {
	Version              int                       `json:"version"`
	DriverRoot           string                    `json:"driver_root"`
	DeliveryID           string                    `json:"delivery_id"`
	HistoryReceipt       string                    `json:"history_receipt"`
	HistoryReceiptSHA256 string                    `json:"history_receipt_sha256"`
	HistoryProofSHA256   string                    `json:"history_proof_sha256"`
	Decisions            map[string]DriverDecision `json:"decisions"`
}

type historyReceipt struct {
	Version            int               `json:"version"`
	Event              string            `json:"event"`
	DriverRoot         string            `json:"driver_root"`
	TaskID             string            `json:"task_id"`
	DeliveryID         string            `json:"delivery_id"`
	HistoryProofSHA256 string            `json:"history_proof_sha256"`
	NativeEventIDs     []string          `json:"native_event_ids"`
	SourceEvents       []json.RawMessage `json:"source_events"`
	NextCursor         string            `json:"next_cursor,omitempty"`
}

type driverStatusRequest struct {
	Version    int    `json:"version"`
	DriverRoot string `json:"driver_root"`
}

var driverIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func loadDriverRequest(path string) (driverRequest, error) {
	var request driverRequest
	if err := readPrivateJSON(path, &request); err != nil {
		return driverRequest{}, err
	}
	launch := LaunchRequest{Version: request.Runtime.Version, Interpreter: request.Runtime.Interpreter,
		InterpreterSHA256: request.Runtime.InterpreterSHA256, PackageRoot: request.Runtime.PackageRoot,
		PackageManifest: request.Runtime.PackageManifest, PackageManifestSHA256: request.Runtime.PackageManifestSHA256}
	if _, err := validateLaunch(launch); err != nil {
		return driverRequest{}, err
	}
	paths := []string{request.DriverRoot, request.ManifestPath, request.PublicSocket, request.BridgeRoot,
		request.OwnerCapabilityRoot, request.G1State, request.Runtime.G0RuntimeRoot,
		request.Runtime.G0RuntimeManifest}
	for _, path := range paths {
		if !absoluteClean(path) {
			return driverRequest{}, codeError("invalid_driver_request")
		}
	}
	if request.Version != 1 || !driverIDPattern.MatchString(request.TaskID) ||
		!driverIDPattern.MatchString(request.ProfileID) || !hexHash(request.ManifestSHA256) ||
		!hexHash(request.OwnerReadySHA256) || request.BootstrapTimeoutSeconds != 300 ||
		request.WatchPolicy != "until_terminal_or_owner_detached" ||
		!regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(request.ActivationID) ||
		!regexp.MustCompile(`^ready-[0-9a-f]{12}\.json$`).MatchString(request.OwnerReadyReceipt) ||
		(request.Mode != "submit" && request.Mode != "rebind") {
		return driverRequest{}, codeError("invalid_driver_request")
	}
	if (request.Mode == "submit" && (!absoluteClean(request.SubmitRequest) || request.ControlFile != "")) ||
		(request.Mode == "rebind" && (!absoluteClean(request.ControlFile) || request.SubmitRequest != "")) {
		return driverRequest{}, codeError("invalid_driver_request")
	}
	if request.EnableTestFake && os.Getenv("ORCHESTRATOR_ENABLE_TEST_FAKE") != "1" {
		return driverRequest{}, codeError("test_fake_not_authorized")
	}
	for _, root := range []string{request.DriverRoot, request.BridgeRoot, request.OwnerCapabilityRoot, request.G1State} {
		if err := validatePrivateDir(root); err != nil {
			return driverRequest{}, err
		}
	}
	return request, nil
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	statValue, ok := infoSys(info)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		!ok || int(statValue.Uid) != os.Getuid() {
		return codeError("driver_root_invalid")
	}
	return nil
}

func driverEnvironment(enableTestFake bool) []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		key := value
		if index := strings.IndexByte(value, '='); index >= 0 {
			key = value[:index]
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "ORCHESTRATOR_") || key == "PYTHONDONTWRITEBYTECODE" {
			continue
		}
		out = append(out, value)
	}
	out = append(out, "PYTHONDONTWRITEBYTECODE=1")
	if enableTestFake {
		out = append(out, "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	}
	if temporary := os.TempDir(); temporary != "" {
		out = append(out, "TMPDIR="+temporary)
	}
	return out
}

func writeDriverJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func readDriverStatus(root string) (json.RawMessage, error) {
	path := filepath.Join(root, "status.json")
	var value map[string]any
	if err := readPrivateJSON(path, &value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	return data, err
}

func StartDriver(ctx context.Context, requestPath string) (json.RawMessage, error) {
	request, err := loadDriverRequest(requestPath)
	if err != nil {
		return nil, err
	}
	launcherPath := filepath.Join(request.DriverRoot, "launcher.json")
	if _, err = os.Lstat(launcherPath); err == nil {
		var existing driverLauncherReceipt
		requestSHA, hashErr := hashRegular(requestPath, false)
		if readErr := readPrivateJSON(launcherPath, &existing); readErr != nil || hashErr != nil ||
			existing.RequestSHA256 != requestSHA {
			return nil, codeError("driver_request_conflict")
		}
		if data, statusErr := readDriverStatus(request.DriverRoot); statusErr == nil {
			var status map[string]any
			if json.Unmarshal(data, &status) != nil {
				return nil, codeError("driver_status_invalid")
			}
			name, _ := status["status"].(string)
			if name == "watching" || name == "awaiting_owner_decision" {
				if current, birthErr := process.Birth(existing.PID); birthErr != nil || current != existing.Birth {
					return json.Marshal(map[string]any{"version": 1, "status": "driver_dead",
						"recoverable": true, "driver_root": request.DriverRoot, "task_id": request.TaskID,
						"previous_status": name})
				}
			}
			return data, nil
		}
		if current, birthErr := process.Birth(existing.PID); birthErr != nil || current != existing.Birth {
			return json.Marshal(map[string]any{"version": 1, "status": "driver_dead",
				"recoverable": true, "driver_root": request.DriverRoot, "task_id": request.TaskID})
		}
		return nil, codeError("driver_starting")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || !absoluteClean(executable) {
		return nil, codeError("driver_launcher_invalid")
	}
	executableSHA, err := hashRegular(executable, true)
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(request.DriverRoot, "driver.log"),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	command := exec.Command(executable, "native-bridge", "driver", "--request", requestPath)
	command.Stdin, command.Stdout, command.Stderr = nil, logFile, logFile
	command.Env = driverEnvironment(request.EnableTestFake)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	_ = logFile.Close()
	birth, err := process.Birth(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		return nil, err
	}
	requestSHA, err := hashRegular(requestPath, false)
	if err != nil {
		_ = command.Process.Kill()
		return nil, err
	}
	receipt := driverLauncherReceipt{Version: 1, PID: command.Process.Pid, Birth: birth,
		Executable: executable, ExecutableSHA256: executableSHA, RequestSHA256: requestSHA}
	if err = writeDriverJSON(launcherPath, receipt); err != nil {
		_ = command.Process.Kill()
		return nil, err
	}
	_ = command.Process.Release()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, statusErr := readDriverStatus(request.DriverRoot); statusErr == nil {
			return data, nil
		}
		if current, birthErr := process.Birth(receipt.PID); birthErr != nil || current != receipt.Birth {
			return nil, codeError("driver_start_failed")
		}
		select {
		case <-ctx.Done():
			stopDriverGroup(receipt)
			return nil, ctx.Err()
		case <-deadline.C:
			stopDriverGroup(receipt)
			return nil, codeError("driver_start_timeout")
		case <-ticker.C:
		}
	}
}

func stopDriverGroup(receipt driverLauncherReceipt) {
	if current, err := process.Birth(receipt.PID); err == nil && current == receipt.Birth {
		_ = syscall.Kill(-receipt.PID, syscall.SIGTERM)
	}
}

func RunDriver(ctx context.Context, requestPath string, stdout, stderr io.Writer) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	request, err := loadDriverRequest(requestPath)
	if err != nil {
		return err
	}
	var launcher driverLauncherReceipt
	launcherPath := filepath.Join(request.DriverRoot, "launcher.json")
	launcherEnd := time.Now().Add(2 * time.Second)
	for {
		err = readPrivateJSON(launcherPath, &launcher)
		if err == nil {
			break
		}
		if time.Now().After(launcherEnd) {
			return codeError("driver_launcher_invalid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil || launcher.Version != 1 || launcher.PID != os.Getpid() || launcher.Birth != birth ||
		launcher.Executable != executable || launcher.RequestSHA256 == "" {
		return codeError("driver_launcher_invalid")
	}
	requestSHA, err := hashRegular(requestPath, false)
	if err != nil || requestSHA != launcher.RequestSHA256 {
		return codeError("driver_launcher_invalid")
	}
	executableSHA, err := hashRegular(executable, true)
	if err != nil || executableSHA != launcher.ExecutableSHA256 {
		return codeError("driver_launcher_invalid")
	}
	launch := LaunchRequest{Version: request.Runtime.Version, Interpreter: request.Runtime.Interpreter,
		InterpreterSHA256: request.Runtime.InterpreterSHA256, PackageRoot: request.Runtime.PackageRoot,
		PackageManifest:       request.Runtime.PackageManifest,
		PackageManifestSHA256: request.Runtime.PackageManifestSHA256}
	script, err := validateLaunch(launch)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, request.Runtime.Interpreter, "-B", script, "driver", "--request", requestPath)
	command.Stdin, command.Stdout, command.Stderr = nil, stdout, stderr
	command.Env = driverEnvironment(request.EnableTestFake)
	if err = command.Run(); err != nil {
		return codeError("native_bridge_driver_failed")
	}
	return nil
}

func RecordDriverDecision(requestPath string) (json.RawMessage, error) {
	var request decisionRequest
	if err := readPrivateJSON(requestPath, &request); err != nil {
		return nil, err
	}
	if request.Version != 1 || !absoluteClean(request.DriverRoot) ||
		!driverIDPattern.MatchString(request.DeliveryID) || !absoluteClean(request.HistoryReceipt) ||
		request.HistoryReceipt != filepath.Join(request.DriverRoot, "history-"+request.DeliveryID+".json") ||
		!hexHash(request.HistoryReceiptSHA256) || !hexHash(request.HistoryProofSHA256) ||
		len(request.Decisions) == 0 || len(request.Decisions) > 8 {
		return nil, codeError("invalid_driver_decision")
	}
	if err := validatePrivateDir(request.DriverRoot); err != nil {
		return nil, err
	}
	if digest, err := hashRegular(request.HistoryReceipt, false); err != nil || digest != request.HistoryReceiptSHA256 {
		return nil, codeError("history_receipt_changed")
	}
	var history historyReceipt
	if err := readPrivateJSON(request.HistoryReceipt, &history); err != nil || history.Version != 1 ||
		history.Event != "history_ready" || history.DriverRoot != request.DriverRoot ||
		history.DeliveryID != request.DeliveryID || history.HistoryProofSHA256 != request.HistoryProofSHA256 ||
		len(history.NativeEventIDs) != len(request.Decisions) {
		return nil, codeError("history_receipt_changed")
	}
	wanted := make(map[string]bool, len(history.NativeEventIDs))
	for _, id := range history.NativeEventIDs {
		if !driverIDPattern.MatchString(id) || wanted[id] {
			return nil, codeError("history_receipt_changed")
		}
		wanted[id] = true
	}
	for id, decision := range request.Decisions {
		if !wanted[id] || !driverIDPattern.MatchString(decision.CommandID) ||
			(decision.Decision != "handled" && decision.Decision != "waiting_user" &&
				decision.Decision != "stale" && decision.Decision != "rejected") {
			return nil, codeError("invalid_driver_decision")
		}
	}
	decisionsDir := filepath.Join(request.DriverRoot, "decisions")
	if err := os.Mkdir(decisionsDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := validatePrivateDir(decisionsDir); err != nil {
		return nil, err
	}
	path := filepath.Join(decisionsDir, request.DeliveryID+".json")
	value := map[string]any{"version": 1, "action": "decide", "delivery_id": request.DeliveryID,
		"history_proof_sha256": request.HistoryProofSHA256, "decisions": request.Decisions}
	if err := writeDriverJSON(path, value); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		var existing map[string]any
		if readErr := readPrivateJSON(path, &existing); readErr != nil {
			return nil, codeError("driver_decision_conflict")
		}
		expected, _ := json.Marshal(value)
		var normalized map[string]any
		_ = json.Unmarshal(expected, &normalized)
		actual, _ := json.Marshal(existing)
		normalizedExpected, _ := json.Marshal(normalized)
		if string(normalizedExpected) != string(actual) {
			return nil, codeError("driver_decision_conflict")
		}
	}
	response, _ := json.Marshal(map[string]any{"version": 1, "status": "decision_recorded",
		"delivery_id": request.DeliveryID})
	return response, nil
}

func DriverStatus(requestPath string) (json.RawMessage, error) {
	var request driverStatusRequest
	if err := readPrivateJSON(requestPath, &request); err != nil {
		return nil, err
	}
	if request.Version != 1 || !absoluteClean(request.DriverRoot) {
		return nil, codeError("invalid_driver_status")
	}
	if err := validatePrivateDir(request.DriverRoot); err != nil {
		return nil, err
	}
	return readDriverStatus(request.DriverRoot)
}
