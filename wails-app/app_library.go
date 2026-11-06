// wails-app/app_library.go
//
// The monoes.me library: log in, browse, add and publish workflows, web
// automations and orgs. Everything shells out to `monoagentcli library …
// --json` and returns its stdout verbatim — the CLI owns the account, the
// downloads, the sha256 checks and the install handoff; this file only
// forwards them.
package main

import (
	"strconv"
	"strings"
	"time"
)

const (
	libraryCLITimeout     = 60 * time.Second
	libraryInstallTimeout = 5 * time.Minute
)

// emitLibraryEvent sends a login progress event to the frontend, named
// "library:login" or "account:login" (tests replace it: a bare context has no
// Wails runtime to emit on).
var emitLibraryEvent = func(a *App, name string, data interface{}) {
	eventsEmit(a.ctx, name, data)
}

// LibraryStatus returns `library status [--offline]`.
func (a *App) LibraryStatus(offline bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "status"}
	if offline {
		args = append(args, "--offline")
	}
	return a.rawCLI(libraryCLITimeout, args...)
}

// The library login is the machine's monoes.me account (one session), so the
// login bindings below are aliases of the Account* ones (app_account.go).
// LibraryLogin keeps its own event name, "library:login", which the library
// dialog listens for.

// LibraryLogin signs in through the browser: AccountLogin, reporting as
// "library:login" events.
func (a *App) LibraryLogin() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runAccountLogin("library:login")
}

// LibraryLoginCancel stops a running LibraryLogin or AccountLogin.
func (a *App) LibraryLoginCancel() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.AccountLoginCancel()
}

// LibraryLoginEmailSend asks monoes.me to email a sign-in code.
func (a *App) LibraryLoginEmailSend(email string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.AccountLoginEmailSend(email)
}

// LibraryLoginEmailVerify trades the emailed code for a session.
func (a *App) LibraryLoginEmailVerify(email, code string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.AccountLoginEmailVerify(email, code)
}

// LibraryLogout ends this machine's monoes.me session.
func (a *App) LibraryLogout() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.AccountLogout()
}

// LibraryList returns `library list` for one kind and scope
// (official | public | mine).
func (a *App) LibraryList(kind, scope, search string, page int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "list"}
	if kind != "" {
		args = append(args, "--kind="+kind)
	}
	if scope != "" {
		args = append(args, "--scope="+scope)
	}
	if s := strings.TrimSpace(search); s != "" {
		args = append(args, "--search="+s)
	}
	if page > 1 {
		args = append(args, "--page="+strconv.Itoa(page))
	}
	return a.rawCLI(libraryCLITimeout, args...)
}

// LibraryShow returns one item (`library show <id|kind/slug>`).
func (a *App) LibraryShow(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.rawCLI(libraryCLITimeout, "library", "show", "--", id)
}

// LibraryInstall adds a library item to this machine. rename installs an
// org under another name; yes confirms replacing what is already there.
func (a *App) LibraryInstall(kind, id, rename string, yes bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "install"}
	if r := strings.TrimSpace(rename); r != "" {
		args = append(args, "--rename="+r)
	}
	if yes {
		args = append(args, "--yes")
	}
	args = append(args, "--", kind, id)
	return a.rawCLI(libraryInstallTimeout, args...)
}

// LibraryPublish packs or exports a local workflow (id), automation (id)
// or org (name) and uploads it — private unless public is set.
func (a *App) LibraryPublish(kind, localID string, public bool, name, description, tags, version string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "publish"}
	if public {
		args = append(args, "--public")
	}
	for _, f := range [][2]string{{"name", name}, {"description", description}, {"tags", tags}, {"version", version}} {
		if v := strings.TrimSpace(f[1]); v != "" {
			args = append(args, "--"+f[0]+"="+v)
		}
	}
	args = append(args, "--", kind, localID)
	return a.rawCLI(libraryInstallTimeout, args...)
}

// LibraryUpdate re-installs newer versions of items that came from the
// library (all of them when id is empty).
func (a *App) LibraryUpdate(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "update", "--yes"}
	if id != "" {
		args = append(args, "--", id)
	}
	return a.rawCLI(libraryInstallTimeout, args...)
}

// LibraryInstalled lists what this machine installed from the library.
func (a *App) LibraryInstalled(kind string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"library", "installed"}
	if kind != "" {
		args = append(args, "--kind="+kind)
	}
	return a.rawCLI(libraryCLITimeout, args...)
}
