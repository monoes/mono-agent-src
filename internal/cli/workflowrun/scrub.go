package workflowrun

// The scrubber for a marked process's run output lives in internal/aitask and
// registers itself when that package is linked in; this keeps it linked in.
import _ "github.com/monoes/mono-agent/internal/aitask"
