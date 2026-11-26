package main

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/ssh"
)

// manifest.json.sshsig is the release key's signature over manifest.json in OpenSSH's SSHSIG format
// (PROTOCOL.sshsig), so install.sh can check it with `ssh-keygen -Y verify` where openssl cannot
// verify Ed25519 (stock macOS). install.sh and scripts/verify-sshsig.sh ask for this namespace and
// for the principal "monoagent-release".
//
// One key signs two formats, which cannot be confused: this one signs bytes that start with
// "SSHSIG", while manifest.json.sig signs a manifest, and a client accepts only valid JSON there.
const (
	sshsigNamespace = "monoagent-manifest"
	sshsigMagic     = "SSHSIG"
	sshsigHash      = "sha512"
	sshsigLineLen   = 70
)

// sshSignature returns the armored SSH signature of message, byte for byte what
// `ssh-keygen -Y sign -n monoagent-manifest` writes for the same key.
func sshSignature(message []byte, priv ed25519.PrivateKey) (string, error) {
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	digest := sha512.Sum512(message)
	signed := ssh.Marshal(struct {
		Namespace, Reserved, HashAlgorithm string
		Hash                               []byte
	}{sshsigNamespace, "", sshsigHash, digest[:]})
	sig := ssh.Marshal(ssh.Signature{
		Format: ssh.KeyAlgoED25519,
		Blob:   ed25519.Sign(priv, append([]byte(sshsigMagic), signed...)),
	})
	blob := ssh.Marshal(struct {
		Version                            uint32
		PublicKey                          []byte
		Namespace, Reserved, HashAlgorithm string
		Signature                          []byte
	}{1, pub.Marshal(), sshsigNamespace, "", sshsigHash, sig})

	text := base64.StdEncoding.EncodeToString(append([]byte(sshsigMagic), blob...))
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(text) > sshsigLineLen {
		out.WriteString(text[:sshsigLineLen] + "\n")
		text = text[sshsigLineLen:]
	}
	out.WriteString(text + "\n-----END SSH SIGNATURE-----\n")
	return out.String(), nil
}
