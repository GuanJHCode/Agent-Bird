package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"
)

type ownerTransactionState uint8

const (
	ownerTransactionActive ownerTransactionState = iota
	ownerTransactionCommitted
	ownerTransactionRolledBack
	ownerTransactionDiscarded
)

// ownerTransaction keeps a physical lease until the owner transition has a
// resolved outcome. Callers hold DB.mu; helpers receive only its sql.Tx.
type ownerTransaction struct {
	conn  *sql.Conn
	tx    *sql.Tx
	state ownerTransactionState
}

func (d *DB) beginOwnerTransaction(ctx context.Context) (*ownerTransaction, error) {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &ownerTransaction{conn: conn, tx: tx}, nil
}

func (t *ownerTransaction) close() {
	if t.state == ownerTransactionActive {
		if err := t.tx.Rollback(); err == nil {
			t.state = ownerTransactionRolledBack
		} else {
			// ErrTxDone can hide database/sql's asynchronous rollback after
			// cancellation. Only our own successful outcome proves reuse safe.
			t.discard()
		}
	}
	_ = t.conn.Close()
}

func (t *ownerTransaction) discard() {
	_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
	t.state = ownerTransactionDiscarded
}

func (t *ownerTransaction) commit() error {
	err := t.tx.Commit()
	if err == nil {
		t.state = ownerTransactionCommitted
		return nil
	}
	// sql.Tx is already done even when SQLite's failed COMMIT leaves an
	// active transaction. Never return this uncertain driver state to the pool.
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = t.conn.ExecContext(cleanup, "ROLLBACK")
	t.discard()
	return err
}
