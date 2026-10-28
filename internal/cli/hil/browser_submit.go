package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// A form submit an agent tried on a page that is not local (internal/browserctl:
// a HIL item of workflow browser.submit) is the operator's to decide, like a send:
// an agent is refused, and an approval is stamped so `browser submit --hil` can
// tell it came from here and not from a path an agent reaches. So is a site an
// agent asked to use (browser.site, level ask): approving it adds an allow rule.

// decideIfBrowserSubmit decides id when it is a browser submit or site item; done is
// false for any other item.
func (d Deps) decideIfBrowserSubmit(cmd *cobra.Command, id string, approve bool) (done bool, err error) {
	db, err := d.OpenDB()
	if err != nil {
		return false, nil
	}
	defer db.Close()
	var nc string
	if err := db.DB.QueryRowContext(cmd.Context(), `SELECT node_config FROM hil_pending WHERE id = ? AND profile_id = ?`,
		id, d.ProfileID()).Scan(&nc); err != nil {
		return false, nil
	}
	var itemID, status string
	if claim, ok := extension.IsPairCard(nc); ok {
		return d.decidePairCard(cmd, db.DB, id, claim, approve)
	}
	switch {
	case browserctl.IsSubmitItem(nc):
		if err := clicore.RequireOperator(d.RequireOperator, "approve or reject a browser form submit"); err != nil {
			return true, err
		}
		it, err := browserctl.DecideSubmitItem(cmd.Context(), db.DB, d.ProfileID(), id, approve)
		if err != nil {
			return true, d.decisionError(id, err)
		}
		itemID, status = it.ID, it.Status
	case browserctl.IsSiteItem(nc):
		// An agent asked to use a site (level ask): approving adds an allow rule.
		if err := clicore.RequireOperator(d.RequireOperator, "let an agent use a site"); err != nil {
			return true, err
		}
		it, err := browserctl.DecideSiteItem(cmd.Context(), db.DB, d.ProfileID(), id, approve)
		if err != nil {
			return true, d.decisionError(id, err)
		}
		itemID, status = it.ID, it.Status
	default:
		return false, nil
	}
	// No workflow run waits on it, so printDecision's daemon hint does not apply.
	if d.JSONOutput() {
		return true, json.NewEncoder(os.Stdout).Encode(map[string]string{"id": itemID, "status": status})
	}
	verb := "Rejected"
	if approve {
		verb = "Approved"
	}
	fmt.Printf("%s HIL item %s\n", verb, itemID)
	return true, nil
}

// decisionError reports an item already decided as not found, as `hil
// approve` does for any item, and a site a block covers as invalid input.
func (d Deps) decisionError(id string, err error) error {
	var se *browserctl.SubmitStateError
	if errors.As(err, &se) {
		return d.NotFound("HIL item %q not found or already resolved", id)
	}
	var blocked *browserctl.SiteBlockedDecisionError
	if errors.As(err, &blocked) {
		return d.InvalidInput("%s", blocked.Error())
	}
	return err
}

// pairCardFields are the card's editable fields: the confirmation code the
// owner typed and the profiles the browser may run, in --data or in the
// card's stored fields.
type pairCardFields struct {
	Code     string  `json:"code"`
	Profiles *string `json:"profiles"`
}

func pairCardInput(cmd *cobra.Command, db *sql.DB, cardID string) pairCardFields {
	var doc pairCardFields
	if f := cmd.Flags().Lookup("data"); f != nil {
		_ = json.Unmarshal([]byte(f.Value.String()), &doc)
	}
	var stored pairCardFields
	var raw string
	if db.QueryRowContext(cmd.Context(), `SELECT editable_data FROM hil_pending WHERE id = ?`, cardID).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &stored)
	}
	if doc.Code == "" {
		doc.Code = stored.Code
	}
	if doc.Profiles == nil {
		doc.Profiles = stored.Profiles
	}
	return doc
}

// pairCardProfiles reads the card's profiles field: profile ids or names,
// comma separated. "any" or "*" as the WHOLE field lets the browser run every
// profile; an empty field limits it to none, and so does a card without the
// field (filed by an older bridge). Each entry goes through profiledir.Resolve:
// an exact id wins, then a name that only one profile has; an unknown or
// shared name is refused, never guessed.
func pairCardProfiles(ctx context.Context, db *sql.DB, field *string) (ids []string, any bool, err error) {
	if field == nil {
		return nil, false, nil
	}
	if whole := strings.TrimSpace(*field); strings.EqualFold(whole, "any") || whole == "*" {
		return nil, true, nil
	}
	for _, p := range strings.Split(*field, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.EqualFold(p, "any") || p == "*" {
			return nil, false, errors.New(`"any" must be the whole field, not one entry of a list`)
		}
		id, err := profiledir.Resolve(ctx, db, p)
		var ambiguous *profiledir.AmbiguousError
		switch {
		case err == nil:
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		case errors.Is(err, profiledir.ErrUnknownProfile):
			return nil, false, fmt.Errorf("profile %q not found (checked both id and name)", p)
		case errors.As(err, &ambiguous):
			return nil, false, fmt.Errorf("%v: type the profile id instead", ambiguous)
		default:
			return nil, false, fmt.Errorf("looking up profile %q: %w", p, err)
		}
	}
	return ids, false, nil
}

// decidePairCard decides a browser's request to pair (a card the bridge filed
// for extension.autoPair=ask). It is the owner's: an agent is refused. Only
// this path and `extension approve` ever approve a claim, so an approval made
// on the row by any other route (the MCP hil tools, the HTTP API) changes
// nothing about the claim.
func (d Deps) decidePairCard(cmd *cobra.Command, db *sql.DB, cardID, claim string, approve bool) (bool, error) {
	if err := clicore.RequireOperator(d.RequireOperator, "approve or deny a browser's request to pair"); err != nil {
		return true, err
	}
	code := ""
	var allowed []string
	var anyProfile bool
	if approve {
		in := pairCardInput(cmd, db, cardID)
		code = in.Code
		var err error
		if allowed, anyProfile, err = pairCardProfiles(cmd.Context(), db, in.Profiles); err != nil {
			return true, d.InvalidInput("%v", err)
		}
	}
	c, err := extension.DecidePendingClaimWith(claim, approve, code, allowed, anyProfile)
	status := "rejected"
	if approve {
		status = "approved"
	}
	if errors.Is(err, extension.ErrBadCode) {
		// Not decided: the card stays for the owner to try again with the code their browser shows.
		return true, d.InvalidInput("%v (type the code in the card's code field, or pass --data '{\"code\":\"ABC-123\"}')", err)
	}
	if err != nil {
		// Gone, expired or already decided: the card has nothing left to decide.
		_ = extension.SettlePairCards(cmd.Context(), db, claim, "expired")
		return true, d.NotFound("HIL item %q not found or already resolved", cardID)
	}
	_ = extension.SettlePairCards(cmd.Context(), db, c.ID, status)
	if d.JSONOutput() {
		return true, json.NewEncoder(os.Stdout).Encode(map[string]string{"id": cardID, "status": status})
	}
	verb := "Denied"
	if approve {
		verb = "Approved"
	}
	fmt.Printf("%s HIL item %s\n", verb, cardID)
	return true, nil
}
