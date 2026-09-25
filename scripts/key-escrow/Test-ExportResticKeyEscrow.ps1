<#
.SYNOPSIS
  Self-test for Export-ResticKeyEscrow.ps1. Needs no Stowline install and no
  administrator rights: it builds throwaway envelopes in the documented
  format with .NET DPAPI and checks the helper reads them back exactly.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'Export-ResticKeyEscrow.ps1')
Add-Type -AssemblyName System.Security

$script:failures = 0
function Assert-That {
    param([bool]$Condition, [string]$Name)
    if ($Condition) { Write-Host "  ok   $Name" } else { Write-Host "  FAIL $Name" -ForegroundColor Red; $script:failures++ }
}

function Test-Throws {
    param([scriptblock]$Block)
    try { & $Block | Out-Null; return $false } catch { return $true }
}

function New-Envelope {
    param([string]$Path, [byte[]]$Plain, [string]$Scope = 'machine')
    $dp = if ($Scope -eq 'machine') { [System.Security.Cryptography.DataProtectionScope]::LocalMachine } else { [System.Security.Cryptography.DataProtectionScope]::CurrentUser }
    $blob = [System.Security.Cryptography.ProtectedData]::Protect($Plain, $null, $dp)
    $head = [System.Text.Encoding]::ASCII.GetBytes("STOWLINE-DPAPI-1`nscope=$Scope`n`n")
    [System.IO.File]::WriteAllBytes($Path, ($head + $blob))
}

function ConvertTo-Secure {
    param([string]$Text)
    $s = New-Object System.Security.SecureString
    foreach ($c in $Text.ToCharArray()) { $s.AppendChar($c) }
    return $s
}

$dir = Join-Path ([System.IO.Path]::GetTempPath()) ("stowline-escrow-test-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $dir | Out-Null
try {
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $random = New-Object byte[] 32
    $rng.GetBytes($random)
    $key = ($random | ForEach-Object { $_.ToString('x2') }) -join ''
    $keyBytes = [System.Text.Encoding]::UTF8.GetBytes($key)

    Write-Host 'envelope decoding'
    $machine = Join-Path $dir 'restic-password.machine.dpapi'
    New-Envelope -Path $machine -Plain $keyBytes -Scope 'machine'
    Assert-That (([System.Text.Encoding]::UTF8.GetString((Get-EscrowKeyBytes -Path $machine))) -eq $key) 'machine-scope envelope round-trips'
    $user = Join-Path $dir 'restic-password.user.dpapi'
    New-Envelope -Path $user -Plain $keyBytes -Scope 'user'
    Assert-That (([System.Text.Encoding]::UTF8.GetString((Get-EscrowKeyBytes -Path $user))) -eq $key) 'user-scope envelope round-trips'

    $bad = Join-Path $dir 'bad.dpapi'
    [System.IO.File]::WriteAllBytes($bad, [System.Text.Encoding]::ASCII.GetBytes($key))
    Assert-That (Test-Throws { Get-EscrowKeyBytes -Path $bad }) 'a raw plaintext file is refused'
    [System.IO.File]::WriteAllBytes($bad, [System.Text.Encoding]::ASCII.GetBytes("NOT-Stowline`nscope=machine`n`nxxxx"))
    Assert-That (Test-Throws { Get-EscrowKeyBytes -Path $bad }) 'a wrong magic header is refused'
    [System.IO.File]::WriteAllBytes($bad, [System.Text.Encoding]::ASCII.GetBytes("STOWLINE-DPAPI-1`nscope=galaxy`n`nxxxx"))
    Assert-That (Test-Throws { Get-EscrowKeyBytes -Path $bad }) 'an unknown scope is refused'

    Write-Host 'envelope selection'
    Assert-That ((Get-EnvelopePath -Dir $dir) -eq $machine) 'machine envelope is preferred over user'
    Remove-Item $machine
    Assert-That ((Get-EnvelopePath -Dir $dir) -eq $user) 'user envelope is the fallback'
    Remove-Item $user
    Assert-That ($null -eq (Get-EnvelopePath -Dir $dir)) 'nothing found gives null'

    Write-Host 'fingerprint'
    $fp = Get-KeyFingerprint -Bytes $keyBytes
    Assert-That ($fp -match '^[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}$') 'has the XXXX-XXXX-XXXX shape'
    Assert-That ($fp -eq (Get-KeyFingerprint -Bytes $keyBytes)) 'is deterministic'
    Assert-That ($fp -ne (Get-KeyFingerprint -Bytes ([System.Text.Encoding]::UTF8.GetBytes($key + 'x')))) 'differs for a different key'

    Write-Host 'control plane address'
    $cfgDir = Join-Path $dir 'config'
    New-Item -ItemType Directory -Path $cfgDir | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $cfgDir 'pilot.json'), '{"control_plane_url":"https://stowline.example.test/"}')
    Assert-That ((Get-ControlPlaneBase -Dir (Join-Path $dir 'secrets')) -eq 'https://stowline.example.test') 'the address comes from pilot.json without a trailing slash'
    [System.IO.File]::WriteAllText((Join-Path $cfgDir 'pilot.json'), '{"control_plane_url":"http://stowline.example.test"}')
    Assert-That (Test-Throws { Get-ControlPlaneBase -Dir (Join-Path $dir 'secrets') }) 'plain http to a remote host is refused'
    Assert-That ((Get-ControlPlaneBase -Dir (Join-Path $dir 'secrets') -Override 'http://127.0.0.1:8099/') -eq 'http://127.0.0.1:8099') 'plain http is allowed only for localhost'
    Assert-That (Test-Throws { Get-ControlPlaneBase -Dir (Join-Path $dir 'nowhere') }) 'a missing pilot.json is an error'

    Write-Host 'upload to the vault'
    $secDir = Join-Path $dir 'secrets'
    New-Item -ItemType Directory -Path $secDir | Out-Null
    $credText = 'test-control-credential-' + [guid]::NewGuid().ToString('N')
    New-Envelope -Path (Join-Path $secDir 'control-credential.machine.dpapi') -Plain ([System.Text.Encoding]::UTF8.GetBytes($credText)) -Scope 'machine'
    $port = Get-Random -Minimum 20000 -Maximum 60000
    $listener = New-Object System.Net.HttpListener
    $listener.Prefixes.Add("http://127.0.0.1:$port/")
    $listener.Start()
    $expectedFp = Get-KeyFingerprint -Bytes $keyBytes
    $ps = [powershell]::Create()
    [void]$ps.AddScript({
        param($l, $fp)
        $ctx = $l.GetContext()
        $rd = New-Object System.IO.StreamReader($ctx.Request.InputStream, [System.Text.Encoding]::UTF8)
        $seen = @{ method = $ctx.Request.HttpMethod; path = $ctx.Request.Url.AbsolutePath; auth = $ctx.Request.Headers['Authorization']; body = $rd.ReadToEnd() }
        $out = [System.Text.Encoding]::UTF8.GetBytes((@{ ok = $true; fingerprint = $fp } | ConvertTo-Json -Compress))
        $ctx.Response.ContentType = 'application/json'
        $ctx.Response.OutputStream.Write($out, 0, $out.Length)
        $ctx.Response.Close()
        return $seen
    }).AddArgument($listener).AddArgument($expectedFp)
    $async = $ps.BeginInvoke()
    $fpBack = Send-KeyToControlPlane -KeyBytes $keyBytes -Dir $secDir -Override "http://127.0.0.1:$port"
    $seen = $ps.EndInvoke($async)[0]
    $listener.Stop()
    Assert-That ($fpBack -eq $expectedFp) 'returns the fingerprint the server confirmed'
    Assert-That ($seen.method -eq 'PUT' -and $seen.path -eq '/api/v1/agent/escrow') 'sends PUT /api/v1/agent/escrow'
    Assert-That ($seen.auth -eq "Bearer $credText") 'authenticates with the decrypted control credential'
    $sent = $seen.body | ConvertFrom-Json
    Assert-That ($sent.kind -eq 'restic-password' -and $sent.secret -eq $key) 'sends the key exactly'
    $port2 = Get-Random -Minimum 20000 -Maximum 60000
    $listener2 = New-Object System.Net.HttpListener
    $listener2.Prefixes.Add("http://127.0.0.1:$port2/")
    $listener2.Start()
    $ps2 = [powershell]::Create()
    [void]$ps2.AddScript({
        param($l)
        $ctx = $l.GetContext()
        $out = [System.Text.Encoding]::UTF8.GetBytes('{"ok":true,"fingerprint":"0000-0000-0000"}')
        $ctx.Response.ContentType = 'application/json'
        $ctx.Response.OutputStream.Write($out, 0, $out.Length)
        $ctx.Response.Close()
    }).AddArgument($listener2)
    $async2 = $ps2.BeginInvoke()
    Assert-That (Test-Throws { Send-KeyToControlPlane -KeyBytes $keyBytes -Dir $secDir -Override "http://127.0.0.1:$port2" }) 'a fingerprint the server does not echo back is an error'
    $ps2.EndInvoke($async2) | Out-Null
    $listener2.Stop()

    Write-Host 'saved-copy check'
    Assert-That (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure $key)) 'exact copy matches'
    Assert-That (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure ($key + "`r`n"))) 'trailing newline is tolerated'
    $grouped = (($key -split '(.{8})' | Where-Object { $_ }) -join ' ')
    Assert-That (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure $grouped)) 'copy typed in 8-character groups matches'
    Assert-That (-not (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure $key.ToUpper()))) 'wrong case is a mismatch (the key is case-sensitive)'
    Assert-That (-not (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure $key.Substring(1)))) 'a truncated copy is a mismatch'
    Assert-That (-not (Test-KeyMatches -Expected $keyBytes -Candidate (ConvertTo-Secure ' '))) 'an empty copy is a mismatch'
}
finally {
    Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
}

if ($script:failures -gt 0) { Write-Host "$script:failures FAILED" -ForegroundColor Red; exit 1 }
Write-Host 'all passed' -ForegroundColor Green
