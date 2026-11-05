package org

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// Signed org definitions (#288, monomind#502): the save that keeps an
// org's signature is orgenv.SaveSigned. `org sign` reviews an org and signs
// it as the operator.

// orgSignResult is `org sign`'s JSON.
type orgSignResult struct {
	V         int    `json:"v"`
	Org       string `json:"org"`
	Supported bool   `json:"supported"`
	State     string `json:"state,omitempty"`
	Detail    string `json:"detail,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	// Hash is the reviewed definition's projection hash (instructions
	// files included): what --expect-hash signs.
	Hash string `json:"hash,omitempty"`
	// BlockedBy names the agent-context marker (CLAUDECODE, ...) this
	// process inherited with no terminal: it can't sign here (the app
	// started from an AI-agent shell), so callers say so up front.
	BlockedBy string `json:"blocked_by,omitempty"`
	Review    string `json:"review,omitempty"`
	Signed    bool   `json:"signed,omitempty"` // this call signed it
	Message   string `json:"message,omitempty"`
}

func newOrgSignCmd(env *orgEnv) *cobra.Command {
	var yes, statusOnly bool
	var expect string
	c := &cobra.Command{
		Use:   "sign <org>",
		Short: "Review an org definition and sign it as the operator (monomind 2.21+)",
		Long: "monomind 2.21 runs an org only when the operator signed its definition (roles, policies, " +
			"runtimes, autonomy, automations; not the goal, status or role titles). mono-agent re-signs " +
			"its own edits of a signed org; an org changed any other way, or never signed, needs this.\n\n" +
			"Without --yes it prints monomind's review (state, each role's authority, what changed since " +
			"the last signature) and, on a terminal, asks before signing; otherwise it signs nothing. " +
			"--yes signs (with --expect-hash, only the definition that was reviewed, instructions files " +
			"included). --status prints " +
			"the signature state alone.\n\n" +
			"Only the operator signs: an org role or agent turn (MONOMIND_ORG_ROLE and the like) can never " +
			"sign, and monomind's refusal is shown as it gives it. The operator's key and signatures live in " +
			"~/.monomind/orgrt-operator, a protected directory hidden from every role; mono-agent does not " +
			"read it from a role either.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			name := args[0]
			if !orgdesign.ValidOrgName(name) {
				return env.d.InvalidInput("invalid org name %q", name)
			}
			root, err := env.RootErr()
			if err != nil {
				return err
			}
			raw, sha, err := orgsign.ReadFile(root, name)
			if errors.Is(err, os.ErrNotExist) {
				return env.d.NotFound("org %q not found under %s", name, orgdesign.OrgsDir(root))
			} else if err != nil {
				return err
			}
			res := orgSignResult{V: 1, Org: name, SHA256: sha}
			if !orgenv.SigningOn(ctx, root) {
				if statusOnly {
					res.Message = "the installed monomind does not require signed org definitions (2.21+)"
					return printJSONValue(res)
				}
				return errOrgSigningUnsupported
			}
			res.Supported = true
			res.BlockedBy = signBlocker()
			st := orgenv.SignStatus(ctx, root, name, raw)
			res.State, res.Detail = st.State, st.Detail
			if statusOnly {
				if !st.OK() {
					res.Message = orgsign.Message(name, st)
				}
				return printJSONValue(res)
			}
			var note string
			if !yes {
				res.Hash, res.Review, note, err = reviewOrg(ctx, root, name)
				if err != nil {
					return err
				}
				if env.d.JSONOutput() || !env.d.StdinIsTerminal() {
					res.Message = "not signed: review the above, then run `monoagentcli org sign " + name + " --yes --expect-hash " + res.Hash + "`"
					if res.Hash == "" {
						res.Message = "not signed: " + note
					}
					return printJSONValue(res)
				}
				// A pty gives a turn a terminal: the gate decides who answers, not the terminal.
				if m := signBlocker(); m != "" {
					return &orgSignBlocked{marker: m, org: name}
				}
				if !confirmOrgSign(cmd.ErrOrStderr(), os.Stdin, name, res.Review) {
					res.Message = "not signed (declined)"
					return printJSONValue(res)
				}
				if res.Hash == "" {
					return env.d.InvalidInput("org %s: not signed: %s", name, note)
				}
				expect = res.Hash // sign exactly what was just reviewed
			}
			if m := orgsign.RoleContextMarker(); m != "" {
				// monomind's own refusal, verbatim, when it gives one.
				if _, rerr := monomind.OrgSignReview(ctx, root, name); rerr != nil {
					return env.d.InvalidInput("%s", rerr.Error())
				}
				return env.d.InvalidInput("refusing to sign: %s is set — this is an org role or agent turn, and only the operator signs org definitions", m)
			}
			// A coding agent (the chat assistant included), whatever a workflow
			// node starts (which a chat or an org can have built) and a coder
			// turn's shell whose markers were cleared (they can all run under a
			// pty, so a terminal proves nothing) never sign: markers or the
			// turn-aware gate refuse --yes (and the confirmation above).
			if m := signBlocker(); m != "" && yes {
				return &orgSignBlocked{marker: m, org: name}
			}
			if cmd.Flags().Changed("expect-hash") && expect == "" {
				// A review that couldn't vouch for its hash hands over "":
				// never read that as "sign whatever is there now".
				return env.d.InvalidInput("org %s: --expect-hash is empty: the review could not tell what it showed — review it again", name)
			}
			if expect == "" {
				expect, _ = orgsign.Hash(root, raw)
			}
			out := orgsign.SignExact(ctx, orgenv.Signer, root, name, expect)
			res.State, res.Detail, res.Signed = out.State, "", out.Signed
			if !out.Signed {
				return &orgSignFailed{org: name, out: out}
			}
			res.Message = "signed; a running org picks it up with `monoagentcli org reload " + name + "`"
			return printJSONValue(res)
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "Sign without asking (required when not on a terminal)")
	c.Flags().StringVar(&expect, "expect-hash", "", "With --yes: sign only if the definition's hash (the review's \"hash\", instructions files included) is this")
	c.Flags().BoolVar(&statusOnly, "status", false, "Print the signature state only")
	return c
}

// reviewOrg is monomind's review of org and the hash of exactly the
// definition it reviewed — or "" with a note saying why that can't be told
// (#295 review).
//   - monomind 2.22 reports that hash itself, from the same read as the
//     review; it is the signing target (its --expect-hash refuses anything
//     else).
//   - On 2.21 it is this package's hash, taken around monomind's read.
//
// The org JSON, every instructions file and the directories above them
// are stamped before and after the review and must not have moved: a file
// or folder swapped in for monomind's read and put back after would
// otherwise show one definition and sign another. Where the stamps can't
// vouch for the files (orgsign.Untrusted) only monomind's own reviewed
// hash, enforced by its --expect-hash, counts. A review during which only
// directory times moved (something writing into .monomind) is run again,
// up to twice; it counts only once it comes back unchanged.
func reviewOrg(ctx context.Context, root, name string) (hash, review, note string, err error) {
	const changed = "the definition changed during the review — review it again"
	for attempt := 0; ; attempt++ {
		before, stampErr := orgsign.StampDefinition(root, name)
		raw, _, readErr := orgsign.ReadFile(root, name)
		h, hashErr := orgsign.Hash(root, raw)
		rv, err := monomind.OrgSignReview(ctx, root, name)
		if err != nil {
			return "", "", "", err
		}
		after, stampErr2 := orgsign.StampDefinition(root, name)
		raw2, _, readErr2 := orgsign.ReadFile(root, name)
		h2, hashErr2 := orgsign.Hash(root, raw2)
		// Judged now, after this attempt's read: a folder turned into a
		// symlink between attempts must not ride on an earlier verdict.
		untrusted := orgsign.Untrusted(root, name)
		goAgrees := func(want string) bool {
			// Where Go can hash the definition, it must equal want at both
			// ends of the read.
			return (hashErr == nil) == (hashErr2 == nil) && (hashErr != nil || (h == want && h2 == want))
		}
		if untrusted != "" {
			if rv.Hash != "" && monomind.OrgSignExpectsHash(ctx, root) && goAgrees(rv.Hash) {
				return rv.Hash, rv.Text, "", nil
			}
			return "", rv.Text, untrusted, nil
		}
		if stampErr != nil || stampErr2 != nil || readErr != nil || readErr2 != nil || !before.Same(after) {
			busy := stampErr == nil && stampErr2 == nil && before.OnlyDirTimesMoved(after) &&
				hashErr == nil && hashErr2 == nil && h == h2
			if busy && attempt < reviewRetries {
				reviewSleep(time.Duration(attempt+1) * reviewRetryBackoff)
				continue
			}
			return "", rv.Text, changed, nil
		}
		if rv.Hash != "" {
			if !goAgrees(rv.Hash) {
				return "", rv.Text, changed, nil
			}
			return rv.Hash, rv.Text, "", nil
		}
		if hashErr != nil || hashErr2 != nil {
			return "", rv.Text, "its hash can't be computed here — sign it with `monomind org sign " + name + "` in a terminal", nil
		}
		if h != h2 {
			return "", rv.Text, changed, nil
		}
		return h, rv.Text, "", nil
	}
}

// reviewRetries and reviewRetryBackoff: a review whose only change was a
// directory time (a running org writing into .monomind) is run again up to
// reviewRetries times, waiting a little longer each time. vars for tests.
var (
	reviewRetries      = 2
	reviewRetryBackoff = 250 * time.Millisecond
	reviewSleep        = time.Sleep
)

// errOrgSigningUnsupported: the installed monomind predates signed org
// definitions, so there is nothing to sign (the GUI ignores this code).
var errOrgSigningUnsupported error = &orgSigningUnsupported{}

type orgSigningUnsupported struct{}

func (*orgSigningUnsupported) Error() string {
	return "org signing needs monomind 2.21 or later (the installed monomind runs orgs unsigned)"
}

func (*orgSigningUnsupported) JSONErrorFields() map[string]any {
	return map[string]any{"code": "org_signing_unsupported"}
}

// signBlocker is what says this process is not the operator's own terminal: its agent-context
// marker, else the turn-aware gate's reason (a coder turn's shell with its markers cleared). "" for the operator.
func signBlocker() string {
	if m := operator.Marker(); m != "" {
		return m
	}
	return opgate.Refusal()
}

// orgSignBlocked is `org sign --yes` refused under an agent-context marker (or
// the turn-aware gate's reason, marker is then that reason) (a terminal does not matter, a pty gives an agent one): a coding agent runs it, or the app was started from an
// AI-agent shell and inherited the marker (it is never stripped).
type orgSignBlocked struct{ marker, org string }

func (e *orgSignBlocked) Error() string {
	return fmt.Sprintf("refusing to sign: %s so an AI agent may be running this "+
		"(or the app was started from an AI-agent shell). Sign it yourself: `monoagentcli org sign %s` in a normal terminal, "+
		"or the app started normally. %s", e.what(), e.org, operator.InheritedHint())
}

// what is the first half of the sentence: a marker is "set", the gate's reason stands on its own.
func (e *orgSignBlocked) what() string {
	if e.marker == operator.Marker() {
		return e.marker + " is set,"
	}
	return e.marker + ","
}

func (e *orgSignBlocked) JSONErrorFields() map[string]any {
	return map[string]any{"code": "org_sign_agent_context", "org": e.org, "blocked_by": e.marker}
}

// orgSignFailed is an `org sign --yes` that did not sign: the notice says
// why (the file changed since the review, monomind refused, ...).
type orgSignFailed struct {
	org string
	out orgsign.Outcome
}

func (e *orgSignFailed) Error() string { return e.out.Notice }

func (e *orgSignFailed) JSONErrorFields() map[string]any {
	return map[string]any{"code": "org_not_signed", "org": e.org, "signature": e.out.State}
}

// confirmOrgSign shows the review on w and asks on in.
func confirmOrgSign(w io.Writer, in io.Reader, name, review string) bool {
	fmt.Fprintln(w, review)
	fmt.Fprintf(w, "\nSign org %q as the operator? [y/N] ", name)
	ans, _ := bufio.NewReader(in).ReadString('\n')
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}
