# Per-profile vault agent access

Approved design: offer Strict, Use without revealing, and Reveal in the CLI and Vault UI. Strict is the default; existing grants migrate to Use. Use permits supported secret references without adding plaintext retrieval. Reveal additionally permits plaintext retrieval through secret reveal and vault.secret_get. Stored secrets remain encrypted. Changes are checked at subsequent reads, including active runs. Only the operator changes the policy; access to internal agent authentication tokens remains reserved. Existing grants retain their secret names, host restrictions and expiry. UI and CLI share the same persisted policy. Work already explicitly approved by the operator retains its existing permissions.

Use masks outputs on supported paths on a best-effort basis; arbitrary agent-written code can transform secrets, so this is an interface permission rather than a sandbox against malicious code.
