# Keep kirocc running on Windows

This optional Windows PowerShell 5.1 script runs kirocc under a current-user
Scheduled Task. It starts at login, restarts the proxy five seconds after any
exit (including exit code zero), and retries the supervisor every minute if it
has stopped. There is no task execution time limit and battery power does not
stop it. The proxy binds only to `127.0.0.1`.

## Install

Install/build `kirocc.exe` and log in to Kiro CLI first. Keep the executable in a
stable location. Stop any manually started proxy on the same port before installing.
From the repository root, in Windows PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\windows\keepalive.ps1 `
  -Action Install -ExecutablePath 'C:\Tools\kirocc\kirocc.exe'
```

The script copies itself and non-secret configuration to
`%LOCALAPPDATA%\kirocc\keepalive`, so the checkout is not needed afterward.
`-ExecutionPolicy Bypass` applies only to these PowerShell processes; it does not
change the machine's execution policy. A machine policy may still prohibit scripts.
Task registration may require administrator approval depending on local policy;
run as the same Windows user who owns the Kiro credentials, not a different admin.

Optional installation parameters:

- `-Port 3457`: use another local port (default `3456`).
- `-DatabasePath 'C:\path\data.sqlite3'`: override the Kiro CLI database.
- `-ApiKeyFile 'C:\private\local-key.txt'`: read the local proxy access key into
  `KIROCC_API_KEY` at each launch. This is the proxy key, not a Kiro API key.
- `-TaskName Kirocc-Other -StateDirectory 'C:\path\other-state'`: separate installation;
  also choose a different port. Pass the same task name and state directory when uninstalling.

Without `-ApiKeyFile`, normal kirocc environment-variable behavior applies. Scheduled
tasks do not inherit temporary variables set only in your terminal. Configure persistent
user environment variables before installation if using them. Do not put credentials in
the checkout or arguments. Restrict the state directory and key file to trusted users;
the installed script runs with your account's permissions.

Installation refuses to overwrite an existing task or configuration. Uninstall before
changing the installation settings; to upgrade the executable, uninstall, replace it,
then reinstall. Task state `Running` means the supervisor is alive, not that upstream
authentication or model requests are succeeding.

## Check and uninstall

```powershell
Get-ScheduledTask -TaskName Kirocc-KeepAlive
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:3456/health
Get-Content "$env:LOCALAPPDATA\kirocc\keepalive\supervisor.log" -Tail 20

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File "$env:LOCALAPPDATA\kirocc\keepalive\keepalive.ps1" -Action Uninstall
```

Uninstall disables/removes the task and stops only the child matching its recorded
PID, executable path, and creation time. Scripts and logs are retained for diagnosis;
configuration is removed so reinstallation is possible. Avoid editing the task manually.

`supervisor.log` records launches and exit codes and keeps one 1 MB backup. `logs/`
retains five launch stdout/stderr pairs; `server.jsonl` uses kirocc's built-in log
rotation. Logs can contain request information; do not publish them without review.

This is a logged-in-user task, not a system service: it cannot serve requests while
the computer is off/asleep or the user is logged out. It restarts exited processes;
it does not kill a running process on a slow or failed upstream request. A port
conflict or invalid credentials must be corrected separately. A supervisor restart
may terminate its recorded surviving child, interrupting an in-flight request.

## Integration test

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\windows\test-keepalive.ps1
```

The test compiles a temporary fake proxy, registers a uniquely named task, checks
normal and abnormal exit recovery and uninstall, then leaves temporary logs for
diagnosis. It does not require Kiro credentials or touch an existing kirocc task.
