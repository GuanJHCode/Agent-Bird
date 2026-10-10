//go:build darwin

package gitfilter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type carrierSocketTestJournal struct{ records []carrierSocketRecord }
func (j *carrierSocketTestJournal) RecordCarrierSocket(_ context.Context, r carrierSocketRecord) error { j.records=append(j.records,r); return nil }

func TestCarrierSocketNativeLifecycle(t *testing.T) {
	segment := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second); defer cancel()
	j := &carrierSocketTestJournal{}
	b := carrierSocketBinding{Original: StreamBinding{ManifestSHA256:"m", ScopeSHA256:"s", ReservationID:"r", AttemptID:"a", PlanRevision:1}, ReservationID:"carrier", Scope:"scope", RuntimeNamespace:"darwin-private-tmp-v1", DirectorySlots:1, SocketSlots:1}
	o, err := newCarrierSocket(ctx, segment, b, j)
	if err != nil || o == nil { t.Fatalf("create owner=%t err=%v",o!=nil,err) }
	if len(j.records) < 2 || j.records[0].Phase!="reserved" || j.records[len(j.records)-1].Phase!="prepared" { t.Fatalf("journal=%+v",j.records) }
	if err:=o.close(ctx); err!=nil { t.Fatal(err) }
	if err:=o.remove(ctx); err!=nil { t.Fatal(err) }
	if _,err:=os.Stat(segment); err!=nil { t.Fatalf("segment changed: %v",err) }
	if _,err:=os.Stat(filepath.Join("/private/tmp", "nonexistent")); err==nil { t.Fatal("fixture collision") }
}
