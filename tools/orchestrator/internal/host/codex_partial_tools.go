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

func codexPartialRead(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (codexsession.ToolResult, error) {
	fields, ok := codexToolArguments(raw, []string{"path"}, []string{"offset", "limit_bytes", "expectedSHA256"})
	if !ok {
		return codexPluginToolFailureResult(), nil
	}
	path, ok := codexToolString(fields, "path")
	if !ok {
		return codexPluginToolFailureResult(), nil
	}
	offset, limit := 0, 4096
	ranged := fields["offset"] != nil || fields["limit_bytes"] != nil
	if fields["offset"] != nil && json.Unmarshal(fields["offset"], &offset) != nil {
		return codexPluginToolFailureResult(), nil
	}
	if fields["limit_bytes"] != nil && json.Unmarshal(fields["limit_bytes"], &limit) != nil {
		return codexPluginToolFailureResult(), nil
	}
	if offset < 0 || limit < 4 || limit > 16384 {
		return codexPluginToolFailureResult(), nil
	}
	content, sum, err := broker.Read(ctx, path)
	if err != nil || !utf8.Valid(content) || ctx.Err() != nil {
		return codexPluginToolFailureResult(), nil
	}
	if fields["expectedSHA256"] != nil {
		expected, ok := codexToolString(fields, "expectedSHA256")
		if !ok || expected != sum {
			return codexPluginToolFailureResult(), nil
		}
	}
	if !ranged {
		if len(content) > codexPluginToolFileBytes {
			return codexPluginToolFailureResult(), nil
		}
		return codexPluginToolJSON(map[string]string{"path": path, "sha256": sum, "content": string(content)})
	}
	if offset > len(content) || (offset < len(content) && !utf8.RuneStart(content[offset])) {
		return codexPluginToolFailureResult(), nil
	}
	end := min(len(content), offset+limit)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return codexPluginToolJSON(map[string]any{"path": path, "sha256": sum, "content": string(content[offset:end]), "file_bytes": len(content), "next_offset": end, "truncated": end < len(content)})
}

func codexSearchFile(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (codexsession.ToolResult, error) {
	fields, ok := codexToolArguments(raw, []string{"path", "query"}, nil)
	if !ok {
		return codexPluginToolFailureResult(), nil
	}
	path, pathOK := codexToolString(fields, "path")
	query, queryOK := codexToolString(fields, "query")
	if !pathOK || !queryOK || len(query) == 0 || len(query) > 256 {
		return codexPluginToolFailureResult(), nil
	}
	content, sum, err := broker.Read(ctx, path)
	if err != nil || !utf8.Valid(content) || ctx.Err() != nil {
		return codexPluginToolFailureResult(), nil
	}
	type match struct {
		Offset int `json:"offset"`
		Line   int `json:"line"`
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
		matches = append(matches, match{start, line})
		cursor = start + len(query)
		line += strings.Count(query, "\n")
	}
	if ctx.Err() != nil {
		return codexPluginToolFailureResult(), nil
	}
	return codexPluginToolJSON(map[string]any{"path": path, "sha256": sum, "file_bytes": len(content), "matches": matches, "truncated": truncated})
}

func codexReplacement(ctx context.Context, broker *codextools.Broker, raw json.RawMessage) (map[string]string, bool) {
	fields, ok := codexToolArguments(raw, []string{"path", "expectedSHA256", "old_text", "new_text"}, nil)
	if !ok {
		return nil, false
	}
	values := map[string]string{}
	for key := range fields {
		value, ok := codexToolString(fields, key)
		if !ok {
			return nil, false
		}
		values[key] = value
	}
	old, replacement := values["old_text"], values["new_text"]
	if old == "" || len(old) > codexPluginToolFileBytes || len(replacement) > codexPluginToolFileBytes {
		return nil, false
	}
	content, sum, err := broker.Read(ctx, values["path"])
	if err != nil || len(content) > codexPluginToolFileBytes || !utf8.Valid(content) || values["expectedSHA256"] != sum || ctx.Err() != nil {
		return nil, false
	}
	start := bytes.Index(content, []byte(old))
	// Count overlapping occurrences too: "aa" in "aaa" is ambiguous.
	if start < 0 || bytes.Contains(content[start+1:], []byte(old)) {
		return nil, false
	}
	if len(content)-len(old)+len(replacement) > codexPluginToolFileBytes {
		return nil, false
	}
	values["content"] = string(content[:start]) + replacement + string(content[start+len(old):])
	return values, true
}
