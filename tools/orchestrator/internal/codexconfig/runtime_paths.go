package codexconfig

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type runtimePaths struct{ sqlite, log string }

// BindRuntimePaths returns an independent readonly projection. It binds exact
// effective-config bytes, not directory identity or file permissions; the Host
// must supply paths derived from its original anchored state layout.
func (p *Projection) BindRuntimePaths(sqlite, log string) (*Projection, error) {
	if p == nil || p.mode != ReadOnly || !validRuntimePath(sqlite) || !validRuntimePath(log) || sqlite == log {
		return nil, ErrUnsupportedContext
	}
	settings := make(map[string]any, len(p.settings))
	for key, value := range p.settings {
		settings[key] = value
	}
	return &Projection{config: append([]byte(nil), p.config...), settings: settings, mode: p.mode, runtimePaths: &runtimePaths{sqlite: sqlite, log: log}}, nil
}

// RuntimePaths reports the immutable binding, without claiming physical scope.
func (p *Projection) RuntimePaths() (sqlite, log string, bound bool) {
	if p == nil || p.runtimePaths == nil {
		return "", "", false
	}
	return p.runtimePaths.sqlite, p.runtimePaths.log, true
}

func validRuntimePath(path string) bool {
	return path != "" && len(path) <= 4096 && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}
