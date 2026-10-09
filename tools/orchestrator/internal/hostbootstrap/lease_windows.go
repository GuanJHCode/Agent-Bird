//go:build windows

package hostbootstrap

import "context"

type Lease struct{}

func Open(context.Context, string, string) (*Lease, error) {
	return nil, Error("host_bootstrap_unsupported")
}
func (*Lease) Close()                                  {}
func (*Lease) Read() (MetadataV2, error)               { return MetadataV2{}, Error("host_bootstrap_unsupported") }
func (*Lease) Snapshot() ([]byte, error)               { return nil, Error("host_bootstrap_unsupported") }
func (*Lease) Unchanged() error                        { return Error("host_bootstrap_unsupported") }
func (*Lease) Publish(MetadataV2) error                { return Error("host_bootstrap_unsupported") }
func (*Lease) PreparePublication([]byte, bool) error   { return Error("host_bootstrap_unsupported") }
func (*Lease) PublishPublication([]byte, string) error { return Error("host_bootstrap_unsupported") }
func (*Lease) VerifyPublication([]byte) error          { return Error("host_bootstrap_unsupported") }
func (*Lease) WriteRecoveryIntent(any) error           { return Error("host_bootstrap_unsupported") }
