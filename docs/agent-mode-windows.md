# Agent mode on Windows

Agent mode keeps a coding agent away from your credentials. On Linux and macOS `monoagentcli agent-mode sandbox` prints a sandbox profile. Windows has no sandbox that a command can start a process in without installing something, so there is no Windows profile. This page describes what to do instead: run the agent as a separate, low-privilege Windows account.

## Why a separate account is the boundary

mono-agent keeps its state in `%USERPROFILE%\.monoagent` and its secrets under your Windows login. Windows does not separate one program of your account from another: anything you start can read what you can read. A different local account is the separation Windows enforces. If that account has no access to your profile folder and to the state folder, a program running as it cannot read them, whatever the program does.

## Set it up

All of this is done once, in an elevated (administrator) PowerShell. Replace the placeholders; nothing here is meant to be run as written.

1. Create the account. Windows asks for the password when the command runs, so it never appears on a command line:

   ```powershell
   net user <AGENT_ACCOUNT> * /add
   ```

   Do not add it to the Administrators group. Do not use your own account's password for it.

2. Find the state folder. `monoagentcli doctor` and `monoagentcli agent-mode status` print it; by default it is `%USERPROFILE%\.monoagent`.

3. Remove the account's access to your data. Your profile folder (`C:\Users\<YOU>`) is closed to other standard accounts by default; the `/remove` commands below make that explicit and also cover the state folder if it was moved. `/inheritance:r` is not used, so your own access and the system's stay as they are:

   ```powershell
   icacls "C:\Users\<YOU>" /remove "<AGENT_ACCOUNT>"
   icacls "<STATE_DIR>" /remove "<AGENT_ACCOUNT>"
   icacls "<STATE_DIR>" /remove "BUILTIN\Users"
   ```

   The last line matters when the state folder sits outside your profile (for example on another drive), where `Users` may be able to read it. Check the result with `icacls "<STATE_DIR>"`.

4. Give the agent a working folder of its own, for example `C:\agent-work\<PROJECT>`, and grant the account modify access to that folder only:

   ```powershell
   icacls "C:\agent-work\<PROJECT>" /grant "<AGENT_ACCOUNT>:(OI)(CI)M"
   ```

## Run the agent as that account

Start the agent with one of these. None of them takes a password on the command line.

- `runas /user:<AGENT_ACCOUNT> "<PATH_TO_AGENT>"` asks for the password at a prompt.
- A scheduled task (Task Scheduler, "Run whether user is logged on or not", the account set in the task). Windows stores the credential; you enter it once in the dialog.
- A Windows Terminal profile whose command line is the `runas` line above.

## Reach mono-agent over loopback with a scoped token

The agent must not run `monoagentcli` as you. It calls the daemon's HTTP API on `127.0.0.1` with a token that opens one profile and a limited set of actions. You make the token as yourself (not as the agent account):

```powershell
monoagentcli --profile <PROFILE> agent-mode token create --name <TOKEN_NAME> --scope read --expires 24h
monoagentcli --profile <PROFILE> agent-mode env --token <TOKEN_NAME>
```

`env` prints the `MONOAGENT_API_URL` and `MONOAGENT_API_TOKEN` settings. Put them in the environment of the agent's process (for a scheduled task, in the task's action; for `runas`, set them in the shell the agent starts from). Scopes are `read`, `write` and `run`; see "Agent mode" in `SECURITY.md`. With `MONOAGENT_API_TOKEN` set, a `monoagentcli` run by the agent sends its commands to the daemon instead of opening your state.

## Check it

```powershell
monoagentcli doctor --agent-account <AGENT_ACCOUNT>
```

The `core.agent_isolation` row lists the access list of the state folder with `icacls` and reports:

- ok: no entry gives the account access;
- warn: it can read the folder, or the list could not be read;
- fail: it can change the folder.

`MONOAGENT_AGENT_ACCOUNT` can be set instead of the flag. The check reads and changes nothing. It counts entries that name the account and entries for groups every local account is in (Everyone, Users, Authenticated Users). It cannot see which other groups the account belongs to, it does not recognise group names in another language, and it does not evaluate deny entries; check those by hand with `icacls`. Without an account the row says it is not configured and is never a failure.

## WSL2 as an alternative

Install the agent and `monoagentcli` inside a WSL2 distribution and use the Linux profile there: `monoagentcli agent-mode sandbox --runtime bwrap --strict`. The agent then runs under the Linux sandbox and cannot see your Windows profile through the distribution's own home folder. mono-agent's data must live inside the distribution to be usable, so it is a second installation with its own state, not a view of the Windows one. Note that WSL2 mounts your Windows drives under `/mnt` by default; the strict profile does not bind them back, but a distribution you configure to share them gives the agent the same view you have.

## What this does not protect against

- Anything the agent account is allowed to do. It can use the scoped token for exactly its scope, read and write its own working folder, and use the network.
- A weak password on the agent account, or an account that is an administrator.
- Folders you or a program granted to `Everyone`, `Users` or `Authenticated Users` outside the two above. Other data of yours that those groups can read, the agent can read.
- A kernel or Windows privilege-escalation flaw. A separate account is an ordinary user boundary, not a virtual machine.
- Windows Sandbox or a virtual machine give a stronger separation than an account, at the price of forwarding the API into them.
- Files you put in the agent's working folder or hand it by other means.
