#Requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Install', 'Run', 'Uninstall')][string]$Action,
    [string]$ExecutablePath,
    [string]$StateDirectory = (Join-Path $env:LOCALAPPDATA 'kirocc\keepalive'),
    [ValidatePattern('^[A-Za-z0-9_-]+$')][string]$TaskName = 'Kirocc-KeepAlive',
    [ValidateRange(1, 65535)][int]$Port = 3456,
    [string]$DatabasePath,
    [string]$ApiKeyFile
)
$ErrorActionPreference = 'Stop'
$StateDirectory = [IO.Path]::GetFullPath($StateDirectory)
$configPath = Join-Path $StateDirectory 'config.json'
$runnerPath = Join-Path $StateDirectory 'keepalive.ps1'
$pidPath = Join-Path $StateDirectory 'child.json'
$taskArguments = '-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $runnerPath + '" -Action Run -StateDirectory "' + $StateDirectory + '" -TaskName "' + $TaskName + '"'

function Stop-OwnedChild($record, $config) {
    if (-not $record) { return }
    $child = Get-Process -Id $record.Id -ErrorAction SilentlyContinue
    if ($child -and $child.Path -eq $config.ExecutablePath -and
        $child.StartTime.ToUniversalTime().Ticks.ToString() -eq $record.StartTicks) {
        Stop-Process -Id $child.Id -Force -ErrorAction SilentlyContinue
    }
}

if ($Action -eq 'Install') {
    if (Get-ScheduledTask -TaskName $TaskName -TaskPath '\' -ErrorAction SilentlyContinue) {
        throw "Task $TaskName already exists. Uninstall it before reinstalling."
    }
    if (Test-Path -LiteralPath $configPath) { throw 'State directory is already configured; uninstall first.' }
    if (-not $ExecutablePath) { throw '-ExecutablePath is required for Install.' }
    $ExecutablePath = (Get-Item -LiteralPath $ExecutablePath -ErrorAction Stop).FullName
    if ([IO.Path]::GetExtension($ExecutablePath) -ne '.exe') { throw 'ExecutablePath must be an .exe file.' }
    if ($DatabasePath) { $DatabasePath = (Get-Item -LiteralPath $DatabasePath -ErrorAction Stop).FullName }
    if ($ApiKeyFile) { $ApiKeyFile = (Get-Item -LiteralPath $ApiKeyFile -ErrorAction Stop).FullName }
    New-Item -ItemType Directory -Path $StateDirectory -Force | Out-Null
    Copy-Item -LiteralPath $PSCommandPath -Destination $runnerPath
    # Store only paths and settings, never API key contents.
    $config = [ordered]@{ ExecutablePath = $ExecutablePath; Port = $Port; DatabasePath = $DatabasePath; ApiKeyFile = $ApiKeyFile; TaskName = $TaskName }
    $config | ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8
    $user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $taskAction = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Argument $taskArguments -WorkingDirectory $StateDirectory
    $triggers = @(
        (New-ScheduledTaskTrigger -AtLogOn -User $user),
        (New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) -RepetitionInterval (New-TimeSpan -Minutes 1))
    )
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
    try {
        Register-ScheduledTask -TaskName $TaskName -TaskPath '\' -Action $taskAction -Trigger $triggers -Settings $settings -Principal $principal -Description 'Keep the local kirocc proxy running while this user is logged in.' | Out-Null
    } catch {
        Remove-Item -LiteralPath $configPath
        throw
    }
    Start-ScheduledTask -TaskName $TaskName -TaskPath '\'
    Write-Output "Installed $TaskName. Logs: $StateDirectory"
    return
}

$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
if ($config.TaskName -ne $TaskName) { throw 'Task name does not match the installed configuration.' }
if ($Action -eq 'Uninstall') {
    $task = Get-ScheduledTask -TaskName $TaskName -TaskPath '\' -ErrorAction SilentlyContinue
    if ($task -and ($task.Actions.Count -ne 1 -or $task.Actions[0].Arguments -ne $taskArguments)) {
        throw 'Task action does not match this installation; refusing to remove it.'
    }
    if ($task) {
        Disable-ScheduledTask -TaskName $TaskName -TaskPath '\' | Out-Null
        Stop-ScheduledTask -TaskName $TaskName -TaskPath '\'
        # Task state alone can report Disabled while an instance is still exiting.
        $scheduler = New-Object -ComObject 'Schedule.Service'
        $scheduler.Connect()
        $registeredTask = $scheduler.GetFolder('\').GetTask($TaskName)
        $deadline = (Get-Date).AddSeconds(15)
        while ($registeredTask.GetInstances(0).Count -gt 0) {
            if ((Get-Date) -gt $deadline) { throw 'Supervisor did not stop; installation retained for retry.' }
            Start-Sleep -Milliseconds 100
        }
    }
    $record = if (Test-Path -LiteralPath $pidPath) { Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json } else { $null }
    Stop-OwnedChild $record $config
    if ($task) {
        Unregister-ScheduledTask -TaskName $TaskName -TaskPath '\' -Confirm:$false
    }
    Remove-Item -LiteralPath $pidPath -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $configPath
    Write-Output "Uninstalled $TaskName. Logs and scripts retained in $StateDirectory."
    return
}

$hash = [Security.Cryptography.SHA256]::Create()
$identity = [BitConverter]::ToString($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($StateDirectory.ToLowerInvariant()))).Replace('-', '')
$hash.Dispose()
$mutex = New-Object Threading.Mutex($false, ('Local\Kirocc-' + $identity))
try { $locked = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $locked = $true }
if (-not $locked) { $mutex.Dispose(); return }
$logs = Join-Path $StateDirectory 'logs'
New-Item -ItemType Directory -Path $logs -Force | Out-Null
function Write-Lifecycle($message) {
    $path = Join-Path $StateDirectory 'supervisor.log'
    if ((Test-Path -LiteralPath $path) -and (Get-Item -LiteralPath $path).Length -gt 1MB) {
        Move-Item -LiteralPath $path -Destination ($path + '.1') -Force
    }
    Add-Content -LiteralPath $path -Value ((Get-Date -Format o) + ' ' + $message)
}
$record = $null
try {
    # Recover a surviving child after an interrupted supervisor, without duplicating it.
    if (Test-Path -LiteralPath $pidPath) {
        Stop-OwnedChild (Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json) $config
    }
    Write-Lifecycle 'Supervisor started.'
    while ($true) {
        try {
            if ($config.ApiKeyFile) {
                $env:KIROCC_API_KEY = [IO.File]::ReadAllText($config.ApiKeyFile).Trim()
                if (-not $env:KIROCC_API_KEY) { throw 'API key file is empty.' }
            }
            $arguments = @('-host', '127.0.0.1', '-port', $config.Port, '-log-file', ('"' + (Join-Path $logs 'server.jsonl') + '"'))
            if ($config.DatabasePath) { $arguments += @('-db', ('"' + $config.DatabasePath + '"')) }
            $stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fff'
            # Keep five stdout/stderr pairs; structured server logs rotate in kirocc itself.
            foreach ($suffix in @('stdout', 'stderr')) {
                Get-ChildItem -LiteralPath $logs -Filter "*.$suffix.log" | Sort-Object Name -Descending | Select-Object -Skip 4 | Remove-Item -Force
            }
            $child = Start-Process -FilePath $config.ExecutablePath -ArgumentList $arguments -WorkingDirectory $StateDirectory -WindowStyle Hidden -RedirectStandardOutput (Join-Path $logs "$stamp.stdout.log") -RedirectStandardError (Join-Path $logs "$stamp.stderr.log") -PassThru
            $null = $child.Handle
            $record = @{ Id = $child.Id; StartTicks = $child.StartTime.ToUniversalTime().Ticks.ToString() }
            $tempPidPath = $pidPath + '.tmp'
            [IO.File]::WriteAllText($tempPidPath, ($record | ConvertTo-Json))
            if (Test-Path -LiteralPath $pidPath) {
                [IO.File]::Replace($tempPidPath, $pidPath, $null)
            } else {
                [IO.File]::Move($tempPidPath, $pidPath)
            }
            Write-Lifecycle ("Started PID=" + $child.Id)
            $child.WaitForExit()
            Write-Lifecycle ("Exited PID=" + $child.Id + " ExitCode=" + $child.ExitCode + '; restart in 5 seconds.')
            $record = $null
            Remove-Item -LiteralPath $pidPath -ErrorAction SilentlyContinue
            $child.Dispose()
        } catch { Write-Lifecycle ('Launch error: ' + $_.Exception.Message) }
        Start-Sleep -Seconds 5
    }
} finally {
    Stop-OwnedChild $record $config
    $mutex.ReleaseMutex()
    $mutex.Dispose()
}
