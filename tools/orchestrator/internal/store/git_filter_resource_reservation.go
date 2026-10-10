package store

import (
	"context"
	"database/sql"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Fail-closed milestone scaffold; no filter resource authority issued yet.
func (d *DB) reserveGitFilter(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	m, err := contract.GitFilterManifestFromPayload(c.AdapterPayload)
	if err != nil {
		return err
	}
	if m != nil || c.GitFilter != nil {
		return contract.ErrGitFilterContract
	}
	return nil
}
func bindGitFilter(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, live bool) error {
	m, err := contract.GitFilterManifestFromPayload(c.AdapterPayload)
	if err != nil {
		return err
	}
	if m != nil || c.GitFilter != nil {
		return contract.ErrGitFilterContract
	}
	return nil
}
func applyGitFilterRelease(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	if e.GitFilterCleanup != nil {
		return contract.ErrGitFilterContract
	}
	return nil
}
