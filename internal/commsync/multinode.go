package commsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// otherNodesPending records that nodeID finished for the message of hilRef and
// reports whether another of its target nodes has not finished yet. Only when
// none is outstanding may the message read as sent; otherwise a later node
// that fails would meet the illegal sent -> failed move and the row would say
// sent for work that partly failed (R16-09).
func otherNodesPending(ctx context.Context, db *sql.DB, hilRef, nodeID string) (bool, error) {
	var targets, finished sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT json_extract(metadata, '$.workflow.target_nodes'), json_extract(metadata, '$.workflow.finished_nodes')
		 FROM person_messages WHERE hil_ref = ?`, hilRef).Scan(&targets, &finished)
	if err != nil {
		return false, fmt.Errorf("commsync: reading the target nodes of %s: %w", hilRef, err)
	}
	var want, done []string
	_ = json.Unmarshal([]byte(targets.String), &want)
	_ = json.Unmarshal([]byte(finished.String), &done)
	seen := map[string]bool{nodeID: true}
	for _, n := range done {
		seen[n] = true
	}
	pending := false
	for _, n := range want {
		if !seen[n] {
			pending = true
		}
	}
	if !pending {
		return false, nil
	}
	done = append(done, nodeID)
	b, _ := json.Marshal(done)
	if _, err := db.ExecContext(ctx,
		`UPDATE person_messages SET metadata = json_set(metadata, '$.workflow.finished_nodes', json(?)) WHERE hil_ref = ?`,
		string(b), hilRef); err != nil {
		return true, fmt.Errorf("commsync: recording that node %s finished for %s: %w", nodeID, hilRef, err)
	}
	return true, nil
}
