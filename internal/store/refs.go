package store

// Composite import edges (spec R-4.7).
//
// A mint may declare `refs: [assetId, ...]` — the inscriptions it is built
// from, in slot order. This table records those edges and nothing else. The
// indexer never fetches a target, never requires one to exist, and no state
// depends on any of it: a ref is a claim about composition, not a dependency.
//
// That restraint is what keeps refs out of consensus. If validity depended on
// the target being present, an indexer that had synced further would accept a
// mint one that had not would reject, and the two would disagree about
// history for a reason that has nothing to do with the chain.

// PutRefs records one asset's import list. Slots are positional and the list
// is written whole, so a replay overwrites rather than accumulating.
func (s *Store) PutRefs(assetID string, height uint64, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM asset_refs WHERE asset_id = ?`, assetID); err != nil {
		return err
	}
	for i, r := range refs {
		if _, err := tx.Exec(
			`INSERT INTO asset_refs (asset_id, idx, ref_id, height) VALUES (?,?,?,?)`,
			assetID, i, r, height); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RefsOf returns an asset's imports in slot order. The slice index is the slot
// number, which is what /content/{id}/{n} resolves.
func (s *Store) RefsOf(assetID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT ref_id FROM asset_refs WHERE asset_id = ? ORDER BY idx ASC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RefAt resolves one slot. Missing means the asset declared fewer imports than
// the slot asked for, which is a client error rather than a dangling edge.
func (s *Store) RefAt(assetID string, idx int) (string, error) {
	var r string
	err := s.db.QueryRow(
		`SELECT ref_id FROM asset_refs WHERE asset_id = ? AND idx = ?`, assetID, idx).Scan(&r)
	if err != nil {
		return "", err
	}
	return r, nil
}

// ReferencedBy lists the assets that import this one — the reverse edge, which
// is the half a client cannot compute for itself without reading every mint.
// Assets hidden by moderation are excluded, the same as any other listing.
func (s *Store) ReferencedBy(refID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT r.asset_id FROM asset_refs r
		  WHERE r.ref_id = ?
		    AND NOT EXISTS (SELECT 1 FROM moderation md
		                     WHERE md.asset_id = r.asset_id AND md.metadata_permitted = 0)
		  ORDER BY r.height DESC LIMIT ?`, refID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
