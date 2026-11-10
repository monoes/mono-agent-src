package missionpursuit

import "github.com/monoes/mono-agent/internal/doomsday/finish"

// SubjectDigest is the digest of the project folder root as the goal check measured it: the same read-only tree digest
// (finish.DigestTree with no copy) the verifier records for a check's subject. It opens no store and writes nothing, and it
// exists so that code outside this package (the goal answer verb) can compare a project with a check without importing the
// doomsday packages.
func SubjectDigest(root string) (string, error) {
	tree, err := finish.DigestTree(root, "")
	if err != nil {
		return "", err
	}
	return tree.Digest, nil
}
