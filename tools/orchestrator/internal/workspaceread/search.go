package workspaceread

import (
	"context"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

func (r *Reader) inventory(ctx context.Context) ([]string, error) {
	paths := []string{}
	count := 0
	var visit func(*os.Root, string, int) error
	visit = func(root *os.Root, prefix string, depth int) error {
		if depth > 32 {
			return CodeError("workspace_scan_limit")
		}
		f, err := root.Open(".")
		if err != nil {
			return CodeError("workspace_path_unavailable")
		}
		defer f.Close()
		for {
			entries, readErr := f.ReadDir(64)
			if readErr != nil && readErr != io.EOF {
				return CodeError("workspace_path_unavailable")
			}
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return CodeError("workspace_read_cancelled")
				}
				count++
				if count > 20_000 || len(paths) >= 10_000 {
					return CodeError("workspace_scan_limit")
				}
				path := prefix + entry.Name()
				if !validPath(path, false) {
					continue
				}
				info, err := root.Lstat(entry.Name())
				if err != nil {
					return CodeError("workspace_path_changed")
				}
				if info.IsDir() {
					child, err := childRoot(root, entry.Name(), info)
					if err != nil {
						return err
					}
					err = visit(child, path+"/", depth+1)
					_ = child.Close()
					if err != nil {
						return err
					}
				} else if ordinaryFile(info) {
					paths = append(paths, path)
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		return nil
	}
	if err := visit(r.root, "", 0); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func inDirectory(path, directory string) bool {
	return directory == "." || strings.HasPrefix(path, directory+"/")
}

type Listing struct {
	Paths     []string `json:"paths"`
	NextAfter string   `json:"next_after,omitempty"`
	Truncated bool     `json:"truncated"`
}

func (r *Reader) List(ctx context.Context, directory, after string, limit int) (Listing, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := Listing{Paths: []string{}}
	if !validPath(directory, true) || (after != "" && !validPath(after, false)) || limit < 1 || limit > 200 {
		return result, CodeError("workspace_list_input_invalid")
	}
	if err := r.check(ctx); err != nil {
		return result, err
	}
	paths, err := r.inventory(ctx)
	if err != nil {
		return result, err
	}
	size := 0
	for _, path := range paths {
		if path <= after || !inDirectory(path, directory) {
			continue
		}
		if len(result.Paths) >= limit || size+len(path)*6+4 > maxOutputBytes {
			result.Truncated = true
			result.NextAfter = result.Paths[len(result.Paths)-1]
			break
		}
		result.Paths = append(result.Paths, path)
		size += len(path)*6 + 4
	}
	return result, r.check(ctx)
}

type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type SearchResult struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
	Skipped   int     `json:"skipped_files"`
}

func (r *Reader) Search(ctx context.Context, pattern, directory, after string, limit int) (SearchResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := SearchResult{Matches: []Match{}}
	if pattern == "" || len(pattern) > 512 || !validPath(directory, true) || (after != "" && !validPath(after, false)) || limit < 1 || limit > 100 {
		return result, CodeError("workspace_search_input_invalid")
	}
	query, err := regexp.Compile(pattern)
	if err != nil {
		return result, CodeError("workspace_search_pattern_invalid")
	}
	if err := r.check(ctx); err != nil {
		return result, err
	}
	paths, err := r.inventory(ctx)
	if err != nil {
		return result, err
	}
	readBytes, outputBytes := 0, 0
	for _, path := range paths {
		if path <= after || !inDirectory(path, directory) {
			continue
		}
		if err := r.check(ctx); err != nil {
			return result, err
		}
		if readBytes >= 8*maxFileBytes {
			result.Truncated = true
			break
		}
		raw, err := r.readFile(path)
		if err == CodeError("workspace_file_too_large") || err == CodeError("workspace_file_not_text") {
			result.Skipped++
			continue
		}
		if err != nil {
			return result, err
		}
		readBytes += len(raw)
		for index, line := range strings.Split(string(raw), "\n") {
			if !query.MatchString(line) {
				continue
			}
			text := strings.TrimSuffix(line, "\r")
			if len(text) > 512 {
				text = string([]rune(text)[:min(128, len([]rune(text)))]) + "…"
			}
			size := (len(path)+len(text))*6 + 100
			if len(result.Matches) >= limit || outputBytes+size > maxOutputBytes {
				result.Truncated = true
				return result, nil
			}
			result.Matches = append(result.Matches, Match{Path: path, Line: index + 1, Text: text})
			outputBytes += size
		}
	}
	return result, r.check(ctx)
}
