// Package releasecli is `monoagentcli release keygen|verify`: the maintainer tools for the signed
// release manifest (internal/release).
package releasecli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/spf13/cobra"
)

// Maintainer tools for the signed release manifest. They are open commands
// (no account needed, like update): they only touch a local keychain entry
// and files the maintainer names.

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// Refusal says why this process is not the operator's own terminal ("" when it is):
	// the CLI's opgate.Refusal. Nil refuses (clicore.OperatorRefusal). `keygen` asks it.
	Refusal func() string
}

// NewCommand returns the `release` group.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Maintainer tools for signed releases (keygen, verify)",
	}
	cmd.AddCommand(newReleaseKeygenCmd(d), newReleaseVerifyCmd())
	return cmd
}

func newReleaseKeygenCmd(d Deps) *cobra.Command {
	var force, stdoutPrivate bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate the release signing key; the private key goes to the OS keychain",
		Long: "Generates an Ed25519 release signing key. The private key is stored in the OS keychain (service monoagent-release-signing, " +
			"account ed25519-v1) and never printed. The public key line is printed: paste it into pinnedReleaseKeys in " +
			"internal/release/keys.go and rebuild; clients trust only the keys pinned there. Run it in your own terminal.\n\n" +
			"An existing key is never replaced unless --force (releases signed with the old key stop verifying in builds that pin only the new one).\n\n" +
			"--stdout-private is for CI signing: nothing is stored in the keychain, the private key (standard base64 of the 64-byte key, one line) goes to STDOUT and the public line to STDERR, " +
			"so it can be piped straight into a secret without showing on screen or in shell history. It refuses to print the private key to a terminal. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli release keygen --stdout-private | gh secret set RELEASE_SIGNING_KEY --env release --repo OWNER/REPO",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("release keygen makes or replaces the release signing key, which only the user decides: %s; run it yourself in your terminal", why)
			}
			if stdoutPrivate {
				return keygenStdoutPrivate(cmd, force)
			}
			if _, err := secrets.ReleaseSigningKey(); err == nil && !force {
				return clicore.InvalidInput("a release signing key already exists in the keychain; --force replaces it")
			} else if err != nil && !errors.Is(err, secrets.ErrNoReleaseKey) {
				return fmt.Errorf("read the keychain: %w", err)
			}
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			if err := secrets.StoreReleaseSigningKey(base64.StdEncoding.EncodeToString(priv)); err != nil {
				return fmt.Errorf("store the private key in the OS keychain: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Private key stored in the OS keychain (monoagent-release-signing / ed25519-v1).")
			fmt.Fprintln(out, "Paste this line into pinnedReleaseKeys in internal/release/keys.go:")
			fmt.Fprintf(out, "\t%q,\n", release.KeyLine(pub))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Replace an existing key")
	cmd.Flags().BoolVar(&stdoutPrivate, "stdout-private", false, "Do not store the key: print the private key to stdout and the public line to stderr (pipe to gh secret set)")
	return cmd
}

func keygenStdoutPrivate(cmd *cobra.Command, force bool) error {
	if force {
		return clicore.InvalidInput("--force does not apply with --stdout-private (nothing is stored)")
	}
	out := cmd.OutOrStdout()
	if f, ok := out.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			return clicore.InvalidInput("stdout is a terminal; pipe it, e.g. | gh secret set RELEASE_SIGNING_KEY --env release --repo OWNER/REPO")
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, base64.StdEncoding.EncodeToString(priv))
	errOut := cmd.ErrOrStderr()
	fmt.Fprintln(errOut, "Private key written to stdout only (not stored anywhere else).")
	fmt.Fprintln(errOut, "WARNING: if you redirected stdout to a file, that file now holds the private key on disk. Delete it as soon as it is in the secret, and create it with umask 077.")
	fmt.Fprintln(errOut, "Paste this line into pinnedReleaseKeys in internal/release/keys.go:")
	fmt.Fprintf(errOut, "\t%q,\n", release.KeyLine(pub))
	return nil
}

func newReleaseVerifyCmd() *cobra.Command {
	var pubkey string
	cmd := &cobra.Command{
		Use:   "verify <manifest> <sig>",
		Short: "Verify a manifest.json against its manifest.json.sig",
		Long: "Checks the signature file (\"<key-id> <base64 signature>\") over the exact bytes of the manifest against the keys pinned " +
			"in this binary, or against --pubkey \"<key-id> <base64 public key>\" (for trying a key before it is pinned). A manifest past its expires_at is refused too. Exit 3 when it does not verify.",
		Example: "  monoagentcli release verify manifest.json manifest.json.sig",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifest, err := os.ReadFile(args[0])
			if err != nil {
				return clicore.NotFound("%v", err)
			}
			sig, err := os.ReadFile(args[1])
			if err != nil {
				return clicore.NotFound("%v", err)
			}
			keys := release.PinnedKeys()
			if pubkey != "" {
				k, err := release.ParseKey(pubkey)
				if err != nil {
					return clicore.InvalidInput("%v", err)
				}
				keys = []release.Key{k}
			}
			if len(keys) == 0 {
				return clicore.InvalidInput("this build pins no release key; pass --pubkey")
			}
			m, err := release.VerifyWith(keys, manifest, sig)
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			if err := m.CheckExpiry(time.Now()); err != nil {
				return clicore.InvalidInput("%v", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "OK: %s, %d assets, signature valid\n", m.Version, len(m.Assets))
			return nil
		},
	}
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "Verify against this \"<key-id> <base64 public key>\" instead of the pinned keys")
	return cmd
}
