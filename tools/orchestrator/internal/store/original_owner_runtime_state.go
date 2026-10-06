package store

import "context"

// CurrentOriginalOwners is coordinator-private startup state, not a public
// enumeration API. Decode/validate every immutable record before key recovery.
func (d *DB) CurrentOriginalOwners(ctx context.Context) ([]OriginalOwnerState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT owner_id FROM execution_owners WHERE is_current=1 ORDER BY owner_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]OriginalOwnerState, 0, len(ids))
	for _, id := range ids {
		row, err := loadOriginalOwner(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, row.state)
	}
	return out, nil
}
