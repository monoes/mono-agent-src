// Package inheritenv names the environment variables that tell a process the coder host starts the number of a pipe
// it inherited (a descriptor on Unix, a handle on Windows). The value is a number, never a secret. The names live here,
// apart from internal/coderhost which reads them, so that a placement that has to give an inherited handle another
// number (the Windows job placement duplicates it into the run's anchor process, internal/doomsday/stop) rewrites
// exactly these variables, found by name and never by value, without importing the code that reads them.
package inheritenv

const (
	// Cred announces the credential frame pipe of a bound turn (coderhost.CredFDEnv).
	Cred = "MONOAGENT_CRED_FD"
	// Mission announces the mission token pipe of a legacy launch (coderhost.MissionFDEnv).
	Mission = "MONOAGENT_MISSION_FD"
)

// Names is every variable that announces an inherited handle.
func Names() []string { return []string{Cred, Mission} }
