# Run with Windows PowerShell 5.1 and permission to register a current-user task.
$ErrorActionPreference = 'Stop'
$script = Join-Path $PSScriptRoot 'keepalive.ps1'
if (-not (Test-Path -LiteralPath $script)) { throw 'keepalive.ps1 is missing' }
$taskName = 'Kirocc-Test-' + [Guid]::NewGuid().ToString('N')
$root = Join-Path ([IO.Path]::GetTempPath()) ($taskName + ' with spaces')
New-Item -ItemType Directory -Path $root | Out-Null
$exe = Join-Path $root 'fake proxy.exe'
$keyFile = Join-Path $root 'test key.txt'
Set-Content -LiteralPath $keyFile -Value 'integration-test-only'
function Assert($condition, $message) { if (-not $condition) { throw $message } }
try {
    Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Threading;
public class FakeProxy {
    public static int Main(string[] args) {
        if (Environment.GetEnvironmentVariable("KIROCC_API_KEY") != "integration-test-only") return 99;
        if (Array.IndexOf(args, "-port") < 0 || Array.IndexOf(args, "127.0.0.1") < 0) return 99;
        int count = File.Exists("count.txt") ? int.Parse(File.ReadAllText("count.txt")) : 0;
        File.WriteAllText("count.txt", (++count).ToString());
        if (count >= 3) Thread.Sleep(60000);
        Thread.Sleep(300);
        return count == 1 ? 17 : 0;
    }
}
'@ -OutputAssembly $exe -OutputType ConsoleApplication
    & $script -Action Install -ExecutablePath $exe -ApiKeyFile $keyFile -StateDirectory (Join-Path $root 'state') -TaskName $taskName
    $config = Get-Content (Join-Path $root 'state\config.json') -Raw
    Assert (-not $config.Contains('integration-test-only')) 'Config must not contain the API key'
    $task = Get-ScheduledTask -TaskName $taskName
    Assert ($task.Settings.ExecutionTimeLimit -eq 'PT0S') 'Task must have no execution time limit'
    Assert (-not $task.Settings.StopIfGoingOnBatteries) 'Task must survive battery power'
    Assert ($task.Settings.MultipleInstances -eq 'IgnoreNew') 'Task must reject parallel runs'
    $duplicateRejected = $false
    try { & $script -Action Install -ExecutablePath $exe -StateDirectory (Join-Path $root 'state') -TaskName $taskName } catch { $duplicateRejected = $true }
    Assert $duplicateRejected 'Install must not overwrite an existing task'
    $log = Join-Path $root 'state\supervisor.log'
    $deadline = (Get-Date).AddSeconds(35)
    do {
        Start-Sleep -Milliseconds 500
        $content = if (Test-Path $log) { Get-Content $log -Raw } else { '' }
        $countPath = Join-Path $root 'state\count.txt'
        $count = if (Test-Path $countPath) { [int](Get-Content $countPath -Raw) } else { 0 }
    } until (($count -ge 3) -or (Get-Date) -gt $deadline)
    Assert ($content -match 'ExitCode=17') 'Nonzero exit must be recorded and restarted'
    Assert ($content -match 'ExitCode=0') 'Normal exit must be recorded and restarted'
    Assert ($count -ge 3) 'A third child must run after both exits'
    & $script -Action Uninstall -StateDirectory (Join-Path $root 'state') -TaskName $taskName
    Assert (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) 'Uninstall must remove the task'
    $running = Get-Process -Name 'fake proxy' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe }
    Assert (-not $running) 'Uninstall must stop the owned child'
    # Exercise uninstall immediately before and after the five-second restart boundary.
    foreach ($delay in @(4900, 5100)) {
        Remove-Item -LiteralPath (Join-Path $root 'state\count.txt') -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $log -ErrorAction SilentlyContinue
        & $script -Action Install -ExecutablePath $exe -ApiKeyFile $keyFile -StateDirectory (Join-Path $root 'state') -TaskName $taskName
        $deadline = (Get-Date).AddSeconds(20)
        do {
            Start-Sleep -Milliseconds 100
            $content = if (Test-Path $log) { Get-Content $log -Raw } else { '' }
        } until ($content -match 'ExitCode=17' -or (Get-Date) -gt $deadline)
        Assert ($content -match 'ExitCode=17') 'Restart race fixture must reach its first exit'
        Start-Sleep -Milliseconds $delay
        & $script -Action Uninstall -StateDirectory (Join-Path $root 'state') -TaskName $taskName
        Assert (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) 'Race uninstall must remove task'
        $running = Get-Process -Name 'fake proxy' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe }
        Assert (-not $running) 'Race uninstall must leave no owned child'
    }
    Write-Output 'PASS: install, spaces, key-file auth, no stored key, duplicate rejection, task settings, nonzero/zero exit recovery, running-child uninstall'
    Write-Output 'PASS: uninstall around restart boundary, reinstall into retained state directory'
} finally {
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        & $script -Action Uninstall -StateDirectory (Join-Path $root 'state') -TaskName $taskName
    }
    Write-Output ('Test artifacts retained for diagnostics: ' + $root)
}
