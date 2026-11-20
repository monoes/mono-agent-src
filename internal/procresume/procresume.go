// Package procresume resumes a Windows process that was created with CREATE_SUSPENDED. os/exec starts a process and
// closes its main thread's handle at once, so the thread cannot be resumed with ResumeThread; NtResumeProcess resumes
// every thread of a process through a process handle with PROCESS_SUSPEND_RESUME (the creator's own handle has it,
// whatever access list the process was created with). It is the one mechanism for the processes this repo starts
// suspended to put them in a Job Object before they run (internal/monomind, internal/doomsday/stop). Windows only.
package procresume
