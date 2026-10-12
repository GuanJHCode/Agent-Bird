package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codextools"
)

// Decode once without letting encoding/json silently accept duplicate keys or
// null scalar values. Resource authorization stays in the anchored Broker.
func codexToolArguments(raw json.RawMessage, required, optional []string) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > 64<<10 || !utf8.Valid(raw) {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, key := range append(append([]string(nil), required...), optional...) {
		allowed[key] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, false
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, false
		}
		name, ok := key.(string)
		if !ok || !allowed[name] || result[name] != nil {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return nil, false
		}
		result[name] = value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return nil, false
	}
	for _, key := range required {
		if result[key] == nil {
			return nil, false
		}
	}
	var extra any
	return result, d.Decode(&extra) == io.EOF
}

func codexToolString(fields map[string]json.RawMessage, name string) (string, bool) {
	var value string
	err := json.Unmarshal(fields[name], &value)
	return value, err == nil && utf8.ValidString(value)
}

// Error codes contain no underlying filesystem paths or OS error text.
func codexToolFailure(code string) (codexsession.ToolResult, error) {
	body, _ := json.Marshal(map[string]string{"failure_reason": code})
	return codexsession.ToolResult{Text: string(body), Success: false}, nil
}

func codexOptionalBytes(fields map[string]json.RawMessage, name string, maximum int) (int, bool) {
	value := 0
	if fields[name] != nil && json.Unmarshal(fields[name], &value) != nil {
		return 0, false
	}
	return value, value == 0 || value >= 4 && value <= maximum
}

// Return a bounded UTF-8 window containing up to one quarter leading context.
func codexContextWindow(content []byte, offset, limit int) (int, int) {
	start := max(0, offset-limit/4)
	for start < offset && !utf8.RuneStart(content[start]) {
		start++
	}
	end := min(len(content), start+limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return start, end
}

func codexPartialRead(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (codexsession.ToolResult, error) {
	fields, ok := codexToolArguments(raw, []string{"path"}, []string{"offset", "limit_bytes", "expectedSHA256"})
	if !ok {
		return codexToolFailure("invalid_arguments")
	}
	path, ok := codexToolString(fields, "path")
	if !ok {
		return codexToolFailure("invalid_arguments")
	}
	offset, limit := 0, 4096
	ranged := fields["offset"] != nil || fields["limit_bytes"] != nil
	if fields["offset"] != nil && json.Unmarshal(fields["offset"], &offset) != nil {
		return codexToolFailure("invalid_arguments")
	}
	if fields["limit_bytes"] != nil && json.Unmarshal(fields["limit_bytes"], &limit) != nil {
		return codexToolFailure("invalid_arguments")
	}
	if offset < 0 || limit < 4 || limit > 16384 {
		return codexToolFailure("invalid_range")
	}
	content, sum, err := broker.Read(ctx, path)
	if ctx.Err() != nil {
		return codexToolFailure("operation_cancelled")
	}
	if err != nil {
		return codexToolFailure("workspace_read_failed")
	}
	if !utf8.Valid(content) {
		return codexToolFailure("invalid_text")
	}
	if fields["expectedSHA256"] != nil {
		expected, ok := codexToolString(fields, "expectedSHA256")
		if !ok {
			return codexToolFailure("invalid_arguments")
		}
		if expected != sum {
			return codexToolFailure("file_changed")
		}
	}
	if !ranged {
		if len(content) > codexPluginToolFileBytes {
			return codexToolFailure("file_too_large")
		}
		return codexPluginToolJSON(map[string]string{"path": path, "sha256": sum, "content": string(content)})
	}
	if offset > len(content) || (offset < len(content) && !utf8.RuneStart(content[offset])) {
		return codexToolFailure("invalid_range")
	}
	end := min(len(content), offset+limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return codexPluginToolJSON(map[string]any{"path": path, "sha256": sum, "content": string(content[offset:end]), "file_bytes": len(content), "next_offset": end, "truncated": end < len(content)})
}

func codexSearchFile(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (codexsession.ToolResult, error) {
	fields, ok := codexToolArguments(raw, []string{"path", "query"}, []string{"context_bytes"})
	if !ok {
		return codexToolFailure("invalid_arguments")
	}
	path, pathOK := codexToolString(fields, "path")
	query, queryOK := codexToolString(fields, "query")
	if !pathOK || !queryOK || len(query) == 0 || len(query) > 256 {
		return codexToolFailure("invalid_arguments")
	}
	contextBytes, ok := codexOptionalBytes(fields, "context_bytes", 256)
	if !ok {
		return codexToolFailure("invalid_range")
	}
	content, sum, err := broker.Read(ctx, path)
	if ctx.Err() != nil {
		return codexToolFailure("operation_cancelled")
	}
	if err != nil {
		return codexToolFailure("workspace_read_failed")
	}
	if !utf8.Valid(content) {
		return codexToolFailure("invalid_text")
	}
	type match struct {
		Offset        int    `json:"offset"`
		Line          int    `json:"line"`
		ContentOffset *int   `json:"content_offset,omitempty"`
		Content       string `json:"content,omitempty"`
	}
	matches := make([]match, 0, 20)
	cursor, line, truncated := 0, 1, false
	for cursor <= len(content) {
		index := bytes.Index(content[cursor:], []byte(query))
		if index < 0 {
			break
		}
		if len(matches) == 20 {
			truncated = true
			break
		}
		start := cursor + index
		line += bytes.Count(content[cursor:start], []byte("\n"))
		item := match{Offset: start, Line: line}
		if contextBytes > 0 {
			lo, hi := codexContextWindow(content, start, contextBytes)
			item.ContentOffset = &lo
			item.Content = string(content[lo:hi])
		}
		matches = append(matches, item)
		cursor = start + len(query)
		line += strings.Count(query, "\n")
	}
	if ctx.Err() != nil {
		return codexToolFailure("operation_cancelled")
	}
	return codexPluginToolJSON(map[string]any{"path": path, "sha256": sum, "file_bytes": len(content), "matches": matches, "truncated": truncated})
}

type codexReplacementPlan struct {
	Path, ExpectedSHA256, Content string
	Offset, VerifyBytes           int
}

func codexReplacement(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (codexReplacementPlan, string) {
	var plan codexReplacementPlan
	fields, ok := codexToolArguments(raw, []string{"path", "expectedSHA256", "old_text", "new_text"}, []string{"verify_bytes"})
	if !ok {
		return plan, "invalid_arguments"
	}
	values := map[string]string{}
	for _, key := range []string{"path", "expectedSHA256", "old_text", "new_text"} {
		value, ok := codexToolString(fields, key)
		if !ok {
			return plan, "invalid_arguments"
		}
		values[key] = value
	}
	plan.VerifyBytes, ok = codexOptionalBytes(fields, "verify_bytes", 1024)
	if !ok {
		return plan, "invalid_range"
	}
	old, replacement := values["old_text"], values["new_text"]
	if old == "" {
		return plan, "invalid_arguments"
	}
	if len(old) > codexPluginToolFileBytes || len(replacement) > codexPluginToolFileBytes {
		return plan, "file_too_large"
	}
	content, sum, err := broker.Read(ctx, values["path"])
	if ctx.Err() != nil {
		return plan, "operation_cancelled"
	}
	if err != nil {
		return plan, "workspace_read_failed"
	}
	if len(content) > codexPluginToolFileBytes {
		return plan, "file_too_large"
	}
	if !utf8.Valid(content) {
		return plan, "invalid_text"
	}
	if values["expectedSHA256"] != sum {
		return plan, "file_changed"
	}
	start := bytes.Index(content, []byte(old))
	if start < 0 {
		return plan, "match_not_found"
	}
	// Count overlapping occurrences too: "aa" in "aaa" is ambiguous.
	if bytes.Contains(content[start+1:], []byte(old)) {
		return plan, "match_ambiguous"
	}
	if len(content)-len(old)+len(replacement) > codexPluginToolFileBytes {
		return plan, "file_too_large"
	}
	plan.Path, plan.ExpectedSHA256 = values["path"], sum
	plan.Content = string(content[:start]) + replacement + string(content[start+len(old):])
	plan.Offset = start
	return plan, ""
}

func codexVerifiedReplacement(ctx context.Context, broker *codextools.Broker, plan codexReplacementPlan, receipt codextools.WriteReceipt) (codexsession.ToolResult, error) {
	content, sum, err := broker.Read(ctx, plan.Path)
	// Do not manufacture a successful readback from the submitted text. Failure
	// after publication preserves the durable write records and aborts the turn.
	if err != nil || ctx.Err() != nil || sum != receipt.SHA256 || !utf8.Valid(content) {
		return codexsession.ToolResult{}, codextools.ErrUnknown
	}
	lo, hi := codexContextWindow(content, min(plan.Offset, len(content)), plan.VerifyBytes)
	return codexPluginToolJSON(map[string]any{"receipt": receipt, "verified": true, "content_offset": lo, "content": string(content[lo:hi]), "file_bytes": len(content)})
}
