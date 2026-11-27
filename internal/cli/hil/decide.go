package hil

import (
	"database/sql"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/hilsend"
)

// ifDigestFlag names the digest `hil list --json` showed for the item being
// decided.
const ifDigestFlag = "if-digest"

func addIfDigest(cmd *cobra.Command) {
	cmd.Flags().String(ifDigestFlag, "", "Decide only while the item is still as hil list --json showed it (its digest)")
}

// ifDigest is the command's --if-digest, "" for none or a command without it.
func ifDigest(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup(ifDigestFlag); f != nil {
		return f.Value.String()
	}
	return ""
}

// twoStepItem says the item is a browser submit, site or pairing card. Its
// content is checked and then it is decided in a second step, so a chat card
// cannot approve it (--if-digest with approve is refused).
func twoStepItem(nodeConfig string) bool {
	_, pair := extension.IsPairCard(nodeConfig)
	return pair || browserctl.IsSubmitItem(nodeConfig) || browserctl.IsSiteItem(nodeConfig)
}

// checkDigest refuses a decision of a pending item that is no longer as the
// caller saw it. It returns the data it checked, to guard the write with.
func (d Deps) checkDigest(cmd *cobra.Command, db *sql.DB, id string) (readonly, editable string, err error) {
	var nc, status string
	err = db.QueryRowContext(cmd.Context(), `SELECT readonly_data, editable_data, COALESCE(node_config, ''), status FROM hil_pending
		WHERE id = ? AND profile_id = ?`, id, d.ProfileID()).Scan(&readonly, &editable, &nc, &status)
	if err != nil || status != "pending" {
		return "", "", d.NotFound("HIL item %q not found or already resolved", id)
	}
	if hilsend.Digest(readonly, editable, nc) != ifDigest(cmd) {
		return "", "", d.InvalidInput("HIL item %q: %v", id, hilsend.ErrChanged)
	}
	return readonly, editable, nil
}

// decideItem writes a workflow item's decision (set, with its arguments) while
// it is pending and, with --if-digest, unchanged: the one update checks both.
func (d Deps) decideItem(cmd *cobra.Command, db *sql.DB, id, set string, args ...any) error {
	query := `UPDATE hil_pending SET ` + set + `, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending' AND profile_id = ?`
	args = append(args, id, d.ProfileID())
	guarded := ifDigest(cmd) != ""
	if guarded {
		readonly, editable, err := d.checkDigest(cmd, db, id)
		if err != nil {
			return err
		}
		query += ` AND readonly_data = ? AND editable_data = ?`
		args = append(args, readonly, editable)
	}
	res, err := db.ExecContext(cmd.Context(), query, args...)
	if err != nil {
		return fmt.Errorf("deciding HIL item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if guarded {
			// It changed between the check and the write.
			if _, _, err := d.checkDigest(cmd, db, id); err != nil {
				return err
			}
		}
		return d.NotFound("HIL item %q not found or already resolved", id)
	}
	return nil
}
