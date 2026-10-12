// Package codextools provides a bounded, descriptor-anchored file broker for a
// trusted Host. It does not grant any process, network, or public CLI access.
package codextools

import (
	"context"
	"errors"
)

const maxFileBytes int64 = 1 << 20

var (
	ErrReadonly = errors.New("codextools_readonly")
	ErrConflict = errors.New("codextools_conflict")
	ErrBudget   = errors.New("codextools_budget_exhausted")
	ErrUnknown  = errors.New("codextools_write_unknown")
)

type WriteIntent struct {
	Path           string
	ExpectedSHA256 string
	ContentSHA256  string
	Bytes          int64
}

type WriteReceipt struct {
	Path   string
	SHA256 string
	Bytes  int64
}

type Options struct {
	Writable        bool
	MaxWrittenBytes int64
	BeforeWrite     func(context.Context, WriteIntent) error
	AfterWrite      func(context.Context, WriteReceipt) error
}
