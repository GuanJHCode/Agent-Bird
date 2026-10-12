package codexsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// FailureCheck distinguishes a check that was never reached from its result.
type FailureCheck string

const (
	FailureNotChecked FailureCheck = "not_checked"
	FailurePassed     FailureCheck = "passed"
	FailureFailed     FailureCheck = "failed"
)

// FailureFacts is one bounded value snapshot of an actual Run rejection. It
// contains no payload, IDs, output, native errors, maps or slice references.
type FailureFacts struct {
	Version        uint8        `json:"version"`
	Phase          string       `json:"phase"`
	Boundary       string       `json:"boundary"`
	Branch         string       `json:"branch"`
	Method         string       `json:"method"`
	MethodSHA256   string       `json:"method_sha256,omitempty"`
	Shape          FailureCheck `json:"shape"`
	Scope          FailureCheck `json:"scope"`
	ItemKind       string       `json:"item_kind"`
	ItemKindSHA256 string       `json:"item_kind_sha256,omitempty"`
	ThreadBound    bool         `json:"thread_bound"`
	TurnBound      bool         `json:"turn_bound"`
	CallbackDone   bool         `json:"callback_done"`
}

type failureError struct {
	cause error
	facts FailureFacts
}

func (e *failureError) Error() string { return e.cause.Error() }
func (e *failureError) Unwrap() error { return e.cause }

// FailureFactsOf returns a copy from the original error chain, including joined
// Host errors. No accessor exposes the wrapper or its mutable construction.
func FailureFactsOf(err error) (FailureFacts, bool) {
	var failure *failureError
	if !errors.As(err, &failure) || failure == nil {
		return FailureFacts{}, false
	}
	return failure.facts, true
}

// These notes are written by the sole Run consumer immediately at the actual
// check, not by the concurrent Call/callback job or a last-event observer.
func (s *sessionState) noteFailure(boundary, branch, method string) {
	name, hash := "none", ""
	if method != "" {
		name = "other"
		// Select the stored pinned key's suffix, never retain the remote string.
		for key := range s.shapes.schemas {
			_, public, ok := strings.Cut(key, ":")
			if ok && public == method {
				name = public
				break
			}
		}
		if name == "other" {
			sum := sha256.Sum256([]byte(method))
			hash = hex.EncodeToString(sum[:])
		}
	}
	s.failure = FailureFacts{Version: 1, Phase: failurePhase(s.phase), Boundary: boundary, Branch: branch, Method: name, MethodSHA256: hash, Shape: FailureNotChecked, Scope: FailureNotChecked, ItemKind: "none"}
}

func failurePhase(phase string) string {
	switch phase {
	case "initialize", "initialized", "config/read", "thread/start", "thread/resume", "binding", "turn/start", "running":
		return phase
	default:
		return "none"
	}
}

func (s *sessionState) failureBranch(boundary, branch string) {
	s.failure.Boundary, s.failure.Branch = boundary, branch
	s.failure.ItemKind, s.failure.ItemKindSHA256 = "none", ""
}

func (s *sessionState) failureItem(kind string) {
	// Fixed ThreadItem enum, including deliberately unsupported native kinds.
	switch kind {
	case "userMessage", "hookPrompt", "agentMessage", "functionCallOutput", "plan", "reasoning", "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "sleep", "imageGeneration", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		s.failure.ItemKind, s.failure.ItemKindSHA256 = kind, ""
	default:
		sum := sha256.Sum256([]byte(kind))
		s.failure.ItemKind, s.failure.ItemKindSHA256 = "other", hex.EncodeToString(sum[:])
	}
}

func (s *sessionState) withFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	facts := s.failure
	facts.ThreadBound, facts.TurnBound, facts.CallbackDone = s.out.Binding.ThreadID != "", s.out.TurnID != "", s.callbackDone
	return &failureError{cause: err, facts: facts}
}
