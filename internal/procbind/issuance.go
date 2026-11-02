package procbind

// IssuanceReady says whether v2 tokens may be issued (the setting setter and the host start gate read it). It is
// false until every legacy path that ends a token also revokes the v2 side in its own transaction and the
// source-walk guard that proves it has merged (design section 7, steps 3d and 3c). TestIssuanceStaysOffUntilTheRevocationGuardsExist
// fails if it is turned on while either is missing.
const IssuanceReady = false
