package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// StoreRootNodes is a copied identity, never an authority to create state.
type StoreRootNodes struct {
	StateRoot                                              string
	StateDevice, StateInode, DatabaseDevice, DatabaseInode uint64
}

// Only authenticated Store resolution may mint a nonzero authority. A copy
// retains the original database identity and checks the current namespace each
// time; it does not treat an existing lock or a newly opened Store as proof.
type StoreRootAuthority struct{ nodes StoreRootNodes }

func (a StoreRootAuthority) Nodes() StoreRootNodes { return a.nodes }
func (a StoreRootAuthority) Validate() error {
	if a.nodes.StateRoot == "" || a.nodes.StateDevice == 0 || a.nodes.StateInode == 0 || a.nodes.DatabaseDevice == 0 || a.nodes.DatabaseInode == 0 {
		return CodeError("owner_initial_recovery_root_invalid")
	}
	current, err := configStoreNodes(filepath.Join(a.nodes.StateRoot, "state.db"))
	if err != nil || initialOwnerRootNodes(current) != a.nodes {
		return CodeError("owner_initial_recovery_root_invalid")
	}
	return nil
}
func initialOwnerRootNodes(a contract.ConfigPreparationAuthority) StoreRootNodes {
	return StoreRootNodes{a.StateRoot, a.StateDevice, a.StateInode, a.DatabaseDevice, a.DatabaseInode}
}
func (d *DB) initialOwnerRootAuthority() (StoreRootAuthority, error) {
	a := StoreRootAuthority{nodes: initialOwnerRootNodes(d.configIdentity)}
	if d.configIdentityError != nil {
		return StoreRootAuthority{}, CodeError("owner_initial_recovery_root_invalid")
	}
	if err := a.Validate(); err != nil {
		return StoreRootAuthority{}, err
	}
	return a, nil
}

type InitialOwnerRecoveryFacts struct {
	Root                                                             StoreRootNodes
	SpoolDevice, SpoolInode, DirectoryDevice, DirectoryInode         uint64
	LockDevice, LockInode, ExistingRecordDevice, ExistingRecordInode uint64
	ScopeSHA256, AuthoritySHA256, RecordSHA256                       string
}

type InitialOwnerRecoveryProof struct {
	Facts     InitialOwnerRecoveryFacts
	StageName string
	Phase     string
}

type InitialOwnerRecoveryContext struct {
	Binding       contract.OriginalOwnerBinding
	RootAuthority StoreRootAuthority
	Proof         *InitialOwnerRecoveryProof
}

// Lease the physical connection before checking its pathname. A lazily opened
// pooled connection must not select a replacement DB after the root check.
// The retained FDs in Host publication and these pre/post checks detect durable
// namespace drift; they are not a custom SQLite VFS or a same-UID ABA defence.
func (d *DB) beginInitialOwnerTransaction(ctx context.Context, selector, value string, required bool) (*ownerTransaction, error) {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*ownerTransaction, error) { _ = conn.Close(); return nil, e }
	var query string
	switch selector {
	case "run":
		query = `SELECT facts_json FROM owner_initial_recoveries WHERE run_id=?`
	case "owner":
		query = `SELECT p.facts_json FROM owner_initial_recoveries p JOIN execution_owners o ON o.launch_id=p.launch_id WHERE o.owner_id=?`
	case "launch":
		query = `SELECT facts_json FROM owner_initial_recoveries WHERE launch_id=?`
	case "authorization":
		query = `SELECT p.facts_json FROM owner_initial_recoveries p JOIN owner_succession_authorizations a ON a.initial_proof_id=p.owner_id WHERE a.authorization_id=?`
	case "command":
		query = `SELECT p.facts_json FROM owner_initial_recoveries p LEFT JOIN owner_succession_authorizations a ON a.initial_proof_id=p.owner_id WHERE p.command_id=? OR a.command_id=?`
	default:
		return fail(originalOwnerRejected())
	}
	args := []any{value}
	if selector == "command" {
		args = append(args, value)
	}
	var body string
	err = conn.QueryRowContext(ctx, query, args...).Scan(&body)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	var authority *StoreRootAuthority
	if err == nil || required {
		a, e := d.initialOwnerRootAuthority()
		if e != nil {
			return fail(e)
		}
		if err == nil {
			var f InitialOwnerRecoveryFacts
			if json.Unmarshal([]byte(body), &f) != nil || !f.valid() || f.Root != a.Nodes() {
				return fail(CodeError("owner_initial_recovery_root_invalid"))
			}
		}
		authority = &a
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	return &ownerTransaction{conn: conn, tx: tx, rootAuthority: authority}, nil
}

func (t *ownerTransaction) finishInitialRead() error {
	if t.state == ownerTransactionActive {
		if err := t.tx.Rollback(); err != nil {
			t.discard()
			return err
		}
		t.state = ownerTransactionRolledBack
	}
	if t.rootAuthority != nil {
		return t.rootAuthority.Validate()
	}
	return nil
}

func (d *DB) CheckInitialOwnerAdmissionRoot(ctx context.Context, hello contract.HostHello) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "launch", hello.LaunchID, false)
	if err != nil {
		return err
	}
	defer owned.close()
	return owned.finishInitialRead()
}
