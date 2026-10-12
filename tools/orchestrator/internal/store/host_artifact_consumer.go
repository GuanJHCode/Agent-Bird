package store

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Read existing owned evidence without acquiring the artifact writer lock while
// a Store transaction is held. This never adopts legacy files or commits refs.
func readCommittedHostArtifact(ctx context.Context, tx *sql.Tx, e contract.Event, limit int64) ([]byte, error) {
	bad := CodeError("host_artifact_unavailable")
	if e.Artifact == nil || e.Artifact.HostArtifact == nil || e.Artifact.Size < 1 || e.Artifact.Size > limit {
		return nil, bad
	}
	var database string
	if tx.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&database) != nil || !filepath.IsAbs(database) {
		return nil, bad
	}
	item, err := authorizeHostArtifact(ctx, tx, database, e, false)
	if err != nil || item == nil {
		return nil, bad
	}
	var saved string
	var manifest int64
	if tx.QueryRowContext(ctx, `SELECT proof_json,manifest_bytes FROM managed_artifact_refs WHERE id=? AND reservation_id=? AND slot='0' AND intake_id IS NULL`, item.proof.IntakeID, item.descriptor.ReservationID).Scan(&saved, &manifest) != nil || saved != item.receipt || manifest != item.manifestBytes {
		return nil, bad
	}
	body, err := artifacts.ReadVerifiedHostSlot(ctx, item.root, item.descriptor, item.proof)
	if err != nil {
		return nil, bad
	}
	return body, nil
}
