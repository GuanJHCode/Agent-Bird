//go:build !darwin && !linux

package codextools

import (
	"context"
	"errors"
)

type Broker struct{}

func Open(string, Options) (*Broker, error) {
	return nil, errors.New("codextools_unsupported_platform")
}
func OpenExclusiveCandidate(string, []string, Options) (*Broker, error) {
	return nil, errors.New("codextools_unsupported_platform")
}
func (*Broker) Read(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("codextools_unsupported_platform")
}
func (*Broker) List(context.Context, string) ([]string, error) {
	return nil, errors.New("codextools_unsupported_platform")
}
func (*Broker) Write(context.Context, string, string, []byte) (WriteReceipt, error) {
	return WriteReceipt{}, errors.New("codextools_unsupported_platform")
}
func (*Broker) Close() error { return nil }
