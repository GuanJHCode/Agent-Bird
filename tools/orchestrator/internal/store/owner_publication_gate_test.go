package store

import (
	"context"
	"testing"
)

func TestPendingPublicationBlocksHostReconcileUntilPublished(t *testing.T) {
	f, spec, publication := publicationFixture(t)
	publication, err := f.db.PrepareOwnerRebindPublication(context.Background(), spec, publication)
	if err != nil {
		t.Fatal(err)
	}
	var host, before string
	if err := f.db.sql.QueryRow(`SELECT h.id,h.status FROM runtime_hosts h JOIN host_launches l ON l.host_id=h.id WHERE l.id=?`, publication.LaunchID).Scan(&host, &before); err != nil {
		t.Fatal(err)
	}
	if err := f.db.BeginHostReconcile(context.Background(), host); err == nil || err.Error() != "owner_rebind_publication_pending" {
		t.Fatalf("pending reconcile error=%v", err)
	}
	var status string
	if err := f.db.sql.QueryRow(`SELECT status FROM runtime_hosts WHERE id=?`, host).Scan(&status); err != nil || status != before {
		t.Fatalf("pending reconcile changed host status=%q err=%v", status, err)
	}
	if err := f.db.FinalizeOwnerRebindPublication(context.Background(), publication); err != nil {
		t.Fatal(err)
	}
	if err := f.db.BeginHostReconcile(context.Background(), host); err != nil {
		t.Fatalf("published reconcile: %v", err)
	}
}
