//go:build !darwin

package process

import "context"

func nativeProcessIdentity(context.Context, string, int) ([]byte, bool, error) {
	return nil, false, nil
}
func nativeProcessGroups(context.Context, string, int) ([]byte, bool, error) { return nil, false, nil }

func nativeAncestryQuery() func(context.Context, string, int) ([]byte, error) { return nil }
