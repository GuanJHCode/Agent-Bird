package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codextools"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"golang.org/x/sys/unix"
)

// The candidate comes only from prepareCandidate for this fresh launch. The
// app-server has the original read-only repository CWD, never candidate write
// permission. Each Host tool call is serialized by the protocol reducer.
func openCodexCandidateTools(ctx context.Context, candidate gitops.PreparedWorkspace, protected codexStateRoot) (*codexsession.HostTools, func() error, error) {
	if err := gitops.ValidatePreparedWorkspace(ctx, candidate); err != nil {
		return nil, nil, err
	}
	sequence := 0
	persist := func(ctx context.Context, phase string, value any) error {
		body, err := json.Marshal(struct {
			Version       int                     `json:"version"`
			Binding       gitops.CandidateBinding `json:"binding"`
			PayloadSHA256 string                  `json:"payload_sha256"`
			Value         any                     `json:"value"`
		}{1, candidate.Binding, candidate.AdapterPayloadSHA256, value})
		if err != nil || len(body) > 8192 {
			return errCodexStateFile
		}
		return codexWriteToolRecord(ctx, protected, fmt.Sprintf("codex-tool-%06d-%s.json", sequence, phase), body)
	}
	broker, err := codextools.OpenExclusiveCandidate(candidate.Receipt.Worktree, candidate.Paths, codextools.Options{Writable: true, MaxWrittenBytes: 512 << 10,
		BeforeWrite: func(ctx context.Context, intent codextools.WriteIntent) error {
			sequence++
			if sequence > 256 {
				return codextools.ErrBudget
			}
			return persist(ctx, "intent", intent)
		},
		AfterWrite: func(ctx context.Context, receipt codextools.WriteReceipt) error {
			return persist(ctx, "receipt", receipt)
		},
	})
	if err != nil {
		return nil, nil, err
	}
	tools := codexBrokerTools(broker)
	read := tools.OnTool
	tools.Writable = true
	tools.OnTool = func(ctx context.Context, call codexsession.ToolCall) (codexsession.ToolResult, error) {
		if call.Tool != "write_file" && call.Tool != "replace_text" {
			return read(ctx, call)
		}
		fields, ok := codexWriteArguments(call.Arguments)
		var replacement codexReplacementPlan
		if call.Tool == "replace_text" {
			var failure string
			replacement, failure = codexReplacement(ctx, broker, call.Arguments)
			if failure != "" {
				return codexToolFailure(failure)
			}
			fields = map[string]string{"path": replacement.Path, "expectedSHA256": replacement.ExpectedSHA256, "content": replacement.Content}
			ok = true
		}
		if !ok {
			return codexToolFailure("invalid_arguments")
		}
		if len(fields["content"]) > codexPluginToolFileBytes {
			return codexToolFailure("file_too_large")
		}
		receipt, err := broker.Write(ctx, fields["path"], fields["expectedSHA256"], []byte(fields["content"]))
		if errors.Is(err, codextools.ErrUnknown) {
			return codexsession.ToolResult{}, err
		}
		if err != nil {
			if errors.Is(err, codextools.ErrBudget) {
				return codexToolFailure("write_budget_exhausted")
			}
			if errors.Is(err, codextools.ErrConflict) {
				return codexToolFailure("write_conflict")
			}
			return codexToolFailure("workspace_write_failed")
		}
		if replacement.VerifyBytes > 0 {
			return codexVerifiedReplacement(ctx, broker, replacement, receipt)
		}
		return codexPluginToolJSON(receipt)
	}
	return tools, broker.Close, nil
}

func codexWriteArguments(raw json.RawMessage) (map[string]string, bool) {
	if len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	result := map[string]string{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok {
			return nil, false
		}
		if _, duplicate := result[name]; duplicate {
			return nil, false
		}
		if name != "path" && name != "expectedSHA256" && name != "content" {
			return nil, false
		}
		var value *string
		if d.Decode(&value) != nil || value == nil || !utf8.ValidString(*value) {
			return nil, false
		}
		result[name] = *value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(result) != 3 {
		return nil, false
	}
	var extra any
	return result, d.Decode(&extra) == io.EOF
}

func codexWriteToolRecord(ctx context.Context, root codexStateRoot, name string, body []byte) error {
	chain, err := codexOpenAnchoredStateRoot(ctx, root)
	if err != nil {
		return err
	}
	defer chain.close()
	fd, err := unix.Openat(chain[len(chain)-1].fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Fsync(chain[len(chain)-1].fd)
	}
	if err == nil {
		err = chain.verify(ctx)
	}
	return err
}
