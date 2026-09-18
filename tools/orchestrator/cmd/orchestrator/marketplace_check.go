package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

// Read-only native catalog preflight. A same-named marketplace is never silently
// moved to another version/source while existing sessions may still depend on it.
func marketplaceSourceState(provider string, raw []byte, destination string) (string, error) {
	type row struct {
		Name              string `json:"name"`
		Source            string `json:"source"`
		Path              string `json:"path"`
		MarketplaceSource struct {
			Type   string `json:"sourceType"`
			Source string `json:"source"`
		} `json:"marketplaceSource"`
	}
	var rows []row
	name := "agent-bird"
	if provider == "codex" {
		var catalog struct {
			Marketplaces *[]row `json:"marketplaces"`
		}
		if json.Unmarshal(raw, &catalog) != nil || catalog.Marketplaces == nil {
			return "", codeError("marketplace_catalog_invalid")
		}
		rows = *catalog.Marketplaces
		name = "codex-bird"
	} else if provider == "claude" {
		if json.Unmarshal(raw, &rows) != nil || rows == nil {
			return "", codeError("marketplace_catalog_invalid")
		}
	} else {
		return "", codeError("invalid_args")
	}
	found := false
	for _, item := range rows {
		if item.Name != name {
			continue
		}
		if found {
			return "", codeError("marketplace_source_conflict")
		}
		found = true
		source := item.Path
		if provider == "codex" {
			if item.MarketplaceSource.Type != "local" {
				return "", codeError("marketplace_source_conflict")
			}
			source = item.MarketplaceSource.Source
		} else if item.Source != "directory" {
			return "", codeError("marketplace_source_conflict")
		}
		if source != destination {
			return "", codeError("marketplace_source_conflict")
		}
	}
	if found {
		return "same", nil
	}
	return "missing", nil
}

func marketplaceCheck(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("marketplace-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	provider := fs.String("provider", "", "codex or claude")
	destination := fs.String("destination", "", "canonical version source directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 || (*provider != "codex" && *provider != "claude") || !filepath.IsAbs(*destination) || filepath.Clean(*destination) != *destination {
		return codeError("invalid_args")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, *provider, "plugin", "marketplace", "list", "--json").Output()
	if err != nil {
		return codeError("marketplace_catalog_unavailable")
	}
	status, err := marketplaceSourceState(*provider, data, *destination)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, status)
	return err
}
