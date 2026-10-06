//go:build !linux

package process

import "context"

func linuxBirth(context.Context, int) (string, error) { return "", CodeError("process_birth_unknown") }
func linuxAncestryContext(context.Context, int) (map[int]string, error) {
	return nil, CodeError("process_ancestry_unknown")
}
func linuxGroupAlive(context.Context, int) (bool, error)       { return false, ErrProcessTreeUnknown }
func linuxGroupQuiescent(context.Context, int) (bool, error)   { return false, ErrProcessTreeUnknown }
func linuxMatchGroupLeader(context.Context, int, string) error { return ErrProcessTreeUnknown }
