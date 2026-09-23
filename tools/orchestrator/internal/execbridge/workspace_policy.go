package execbridge

import (
	"context"
	"encoding/base64"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/workspaceread"
)

type workspacePolicy struct {
	ctx       context.Context
	directory string
	reader    *workspaceread.Reader
	writable  bool
	protected []string
}

func newWorkspacePolicy(ctx context.Context, directory string, writable bool, protected []string) (*workspacePolicy, error) {
	reader, err := workspaceread.Open(directory)
	if err != nil {
		return nil, err
	}
	return &workspacePolicy{ctx: ctx, directory: directory, reader: reader, writable: writable, protected: append([]string{}, protected...)}, nil
}
func (p *workspacePolicy) close() error { return p.reader.Close() }

func (p *workspacePolicy) check(method string, params map[string]any) error {
	writes, directory := false, false
	fields := map[string]bool{"path": true, "sandbox": true, "followSymlinks": true}
	switch method {
	case "fs/getMetadata":
		directory = true
	case "fs/readFile":
	case "fs/writeFile":
		writes = true
		fields["dataBase64"] = true
		value, ok := params["dataBase64"].(string)
		if !ok || len(value) > maxFrame {
			return errPolicy
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || base64.StdEncoding.EncodeToString(raw) != value {
			return errPolicy
		}
	case "fs/createDirectory":
		writes, directory = true, true
		fields["recursive"] = true
		if params["recursive"] != true {
			return errPolicy
		}
	case "fs/remove":
		writes = true
		fields["recursive"], fields["force"] = true, true
		for _, key := range []string{"recursive", "force"} {
			if params[key] != false {
				return errPolicy
			}
		}
	default:
		return errPolicy
	}
	if writes && !p.writable {
		return errPolicy
	}
	for key := range params {
		if !fields[key] {
			return errPolicy
		}
	}
	if follow, exists := params["followSymlinks"]; exists && follow != false {
		return errPolicy
	}
	if _, exists := params["sandbox"]; !exists {
		return errPolicy
	}
	// Fixed native patch verification reads use External; execution uses null.
	// Neither value grants filesystem access: the anchored path gate, native
	// no-follow operations and the executor's outer sandbox enforce that scope.
	if sandbox := params["sandbox"]; sandbox != nil {
		if method != "fs/readFile" || validateExternal(sandbox) != nil {
			return errPolicy
		}
	}
	raw, ok := params["path"].(string)
	if !ok {
		return errPolicy
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path || raw != (&url.URL{Scheme: "file", Path: u.Path}).String() {
		return errPolicy
	}
	if writes {
		for _, path := range p.protected {
			if strings.EqualFold(u.Path, path) || strings.HasPrefix(strings.ToLower(u.Path), strings.ToLower(path)+string(filepath.Separator)) {
				return errPolicy
			}
		}
	}
	relative, err := filepath.Rel(p.directory, u.Path)
	if err != nil || p.reader.CheckNativePath(p.ctx, relative, true, directory) != nil {
		return errPolicy
	}
	return nil
}
