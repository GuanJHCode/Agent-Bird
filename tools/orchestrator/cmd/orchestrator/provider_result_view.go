package main

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

const providerResultPreviewBytes = 4096

// The durable call contract remains unchanged. Only MCP's model-facing view
// is bounded. Details are scoped to this connection, with no arbitrary file IO.
type birdResultView struct {
	providerCallResult
	ResultTruncated bool   `json:"result_truncated"`
	ResultBytes     int    `json:"result_bytes"`
	ResultSHA256    string `json:"result_sha256"`
	NextOffset      int    `json:"next_offset"`
}
type birdResultRequest struct {
	CallID     string `json:"call_id"`
	Offset     int    `json:"offset,omitempty"`
	LimitBytes int    `json:"limit_bytes,omitempty"`
}
type birdResultPage struct {
	Version      int    `json:"version"`
	CallID       string `json:"call_id"`
	Result       string `json:"result"`
	ResultBytes  int    `json:"result_bytes"`
	ResultSHA256 string `json:"result_sha256"`
	NextOffset   int    `json:"next_offset"`
}

func resultEnd(text string, offset, limit int) int {
	end := offset + limit
	if end >= len(text) {
		return len(text)
	}
	for end > offset && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}
func resultDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func compactProviderResult(result providerCallResult) birdResultView {
	end := resultEnd(result.Result, 0, providerResultPreviewBytes)
	view := birdResultView{providerCallResult: result, ResultTruncated: end < len(result.Result), ResultBytes: len(result.Result), ResultSHA256: resultDigest(result.Result), NextOffset: end}
	view.Result = result.Result[:end]
	view.TimingsMS = nil // Private diagnostics do not consume model context.
	return view
}
func providerResultPage(result *providerCallResult, req birdResultRequest) (birdResultPage, error) {
	if result == nil || req.CallID == "" || req.CallID != result.CallID {
		return birdResultPage{}, codeError("call_result_not_available")
	}
	if req.LimitBytes == 0 {
		req.LimitBytes = providerResultPreviewBytes
	}
	if req.LimitBytes < 4 || req.LimitBytes > 16384 || req.Offset < 0 || req.Offset > len(result.Result) || req.Offset < len(result.Result) && !utf8.RuneStart(result.Result[req.Offset]) {
		return birdResultPage{}, codeError("call_result_request_invalid")
	}
	end := resultEnd(result.Result, req.Offset, req.LimitBytes)
	return birdResultPage{Version: 1, CallID: req.CallID, Result: result.Result[req.Offset:end], ResultBytes: len(result.Result), ResultSHA256: resultDigest(result.Result), NextOffset: end}, nil
}

const birdResultSchema = `{"type":"object","required":["call_id"],"additionalProperties":false,"properties":{"call_id":{"type":"string"},"offset":{"type":"integer","minimum":0,"default":0},"limit_bytes":{"type":"integer","minimum":4,"maximum":16384,"default":4096}}}`
