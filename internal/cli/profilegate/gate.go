package profilegate

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// globalWhenFlag lists scoped commands that are machine-pure when one flag is
// given, keyed by command path (as in classes): `workflow validate --file`
// reads a file and touches no profile data, while `workflow validate <id>`
// loads the workflow from a profile. --all-profiles is a deliberate
// cross-profile read: it names every profile, so --profile would contradict it.
var globalWhenFlag = map[string]string{
	"workflow validate":       "file",
	"summary":                 "all-profiles",
	"org summary":             "all-profiles",
	"workflow list":           "all-profiles",
	"workflow executions":     "all-profiles",
	"api key list":            "all-profiles",
	"capture list":            "all-profiles",
	"profile documents index": "all-profiles",
	// The user's cross-profile list of pending approvals; the command refuses
	// whatever is not the operator.
	"hil pending": "all",
}

// Mode is what the gate does with a scoped command that names no profile.
type Mode int

const (
	// ModeWarn lets the command run and returns a warning to print.
	ModeWarn Mode = iota
	// ModeEnforce refuses the command.
	ModeEnforce
)

// ProfileRef is one profile as the gate lists it in its message.
type ProfileRef struct{ ID, Name string }

// Lister names the profiles that exist. It is best effort: the gate omits the
// list when there is no Lister or it fails.
type Lister func() ([]ProfileRef, error)

// Check judges one invocation of c. flagSet is whether --profile was given on
// the command line (cmd.Flags().Changed("profile")), read before anything
// resolves a default. A global command, help, completion and the root pass
// silently. A scoped command without the flag returns the warning in ModeWarn
// and an error in ModeEnforce.
func Check(c *cobra.Command, flagSet bool, mode Mode, list Lister) (warning string, err error) {
	if flagSet || exempt(c) || Class(c) == ClassGlobal || globalByFlag(c) {
		return "", nil
	}
	msg := "this command acts on a profile: pass --profile <id|name>"
	if refs, known := listProfiles(list); known && len(refs) == 0 {
		msg += ". No profiles exist yet: create one with `monoagentcli profile create <name>`"
	} else if len(refs) > 0 {
		parts := make([]string, len(refs))
		for i, r := range refs {
			parts[i] = fmt.Sprintf("%s (%s)", r.Name, r.ID)
		}
		msg += ". Profiles: " + strings.Join(parts, ", ")
	}
	if mode == ModeEnforce {
		return "", fmt.Errorf("%s", msg)
	}
	return "warning: " + msg + "; warn mode: the command runs without a profile", nil
}

// globalByFlag is whether c was given the flag that makes it machine-pure.
func globalByFlag(c *cobra.Command) bool {
	flag, ok := globalWhenFlag[CommandKey(c)]
	return ok && c.Flags().Changed(flag)
}

// exempt is what the gate never judges: the root, a command that does no work
// (a group that only prints help or rejects an unknown subcommand, or one that
// is not runnable), and cobra's own help and completion commands, which cobra
// adds while a command runs, so the classification table never sees them.
func exempt(c *cobra.Command) bool {
	if !c.HasParent() || !c.Runnable() || c.Annotations[GroupAnnotation] != "" {
		return true
	}
	for ; c != nil && c.HasParent(); c = c.Parent() {
		switch c.Name() {
		case "help", "completion", "__complete", "__completeNoDesc":
			return true
		}
	}
	return false
}

func listProfiles(list Lister) (refs []ProfileRef, known bool) {
	if list == nil {
		return nil, false
	}
	refs, err := list()
	if err != nil {
		return nil, false
	}
	return refs, true
}
