package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var entered = make(chan struct{})
var release = make(chan struct{})

type drv struct{}
type conn struct{}
type tx struct{}

func (drv) Open(string) (driver.Conn, error)                              { return conn{}, nil }
func (conn) Prepare(string) (driver.Stmt, error)                          { return nil, errors.New("not used") }
func (conn) Close() error                                                 { return nil }
func (conn) Begin() (driver.Tx, error)                                    { return tx{}, nil }
func (conn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return tx{}, nil }
func (conn) ResetSession(context.Context) error                           { return nil }
func (conn) IsValid() bool                                                { return true }
func (tx) Commit() error                                                  { return errors.New("not used") }
func (tx) Rollback() error                                                { close(entered); <-release; return nil }
func main() {
	sql.Register("controlled-rollback", drv{})
	db, e := sql.Open("controlled-rollback", "")
	if e != nil {
		panic(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	t, e := db.BeginTx(ctx, nil)
	if e != nil {
		panic(e)
	}
	cancel()
	<-entered
	e = t.Rollback()
	before := db.Stats().InUse
	if !errors.Is(e, sql.ErrTxDone) || before != 1 {
		panic(fmt.Sprintf("missing race: %v %d", e, before))
	}
	ready := make(chan error, 1)
	go func() {
		cctx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		c, e := db.Conn(cctx)
		if e == nil {
			e = c.Close()
		}
		ready <- e
	}()
	select {
	case e := <-ready:
		panic(fmt.Sprintf("barrier did not wait: %v", e))
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if e = <-ready; e != nil {
		panic(e)
	}
	after := db.Stats().InUse
	if after != 0 {
		panic("retained connection")
	}
	out, _ := json.Marshal(map[string]any{"rollback_returned_err_tx_done": true, "connection_in_use_after_return": before, "connection_in_use_after_bounded_pool_barrier": after, "model_called": false})
	fmt.Println(string(out))
}
