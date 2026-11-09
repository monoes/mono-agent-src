//go:build !windows

package chat

import "context"

func stopHostOS(_ *Deps, info hostInfo) (bool, error) { return false, terminateProcess(info.PID) }

// watchHostStop: a Unix host stops on SIGTERM (host run's signal context); there is nothing more to watch.
func watchHostStop(context.Context, string, func()) (func(), error) { return func() {}, nil }
