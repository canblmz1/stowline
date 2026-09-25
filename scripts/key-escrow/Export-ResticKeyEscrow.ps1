<#
.SYNOPSIS
  Shows this PC's Stowline backup key once so an administrator can store it
  offline, then checks the stored copy is exact.

.DESCRIPTION
  The Restic repository password is generated on the endpoint and kept only
  in a Windows DPAPI envelope (C:\Stowline\secrets\restic-password.*.dpapi).
  Neither the control plane, the gateway nor the installer holds a copy, so
  losing this PC loses every backup unless the key was escrowed elsewhere.

  Read-only: nothing is written to disk, the service is not touched, the key
  is not changed, and no reinstall is needed. Run it elevated on the endpoint.

  -Verify skips the display and only checks a saved copy against this PC.

  After the key is saved the tool can also store a sealed copy in the Stowline
  control plane's Sifre Kasasi (Password Vault), authenticating with this PC's
  own control credential, so the operator can see it in the admin panel if the
  PC is ever lost. -UploadOnly does just that without showing the key;
  -NoUpload never sends anything.
#>
[CmdletBinding()]
param(
    [string]$SecretsDir = 'C:\Stowline\secrets',
    [switch]$Verify,
    [switch]$UploadOnly,
    [switch]$NoUpload,
    [switch]$Yes,
    [string]$ControlPlaneUrl = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Script:EnvelopeMagic = 'STOWLINE-DPAPI-1'


# Messages follow the Windows display language: Turkish, else English.
$script:TR = [System.Globalization.CultureInfo]::CurrentUICulture.Name -like 'tr*'
function L([string]$tr, [string]$en) { if ($script:TR) { $tr } else { $en } }

function Test-IsAdministrator {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-EnvelopePath {
    param([Parameter(Mandatory)][string]$Dir, [string[]]$Names = @('restic-password.machine.dpapi', 'restic-password.user.dpapi'))
    foreach ($name in $Names) {
        $candidate = Join-Path $Dir $name
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    return $null
}

# Decrypts an envelope exactly as agent/internal/adapters/secrets/dpapi_windows.go
# writes it: "STOWLINE-DPAPI-1\nscope=<machine|user>\n\n<raw DPAPI blob>", no entropy.
function Get-EscrowKeyBytes {
    param([Parameter(Mandatory)][string]$Path)
    Add-Type -AssemblyName System.Security
    $raw = [System.IO.File]::ReadAllBytes($Path)
    $sep = -1
    for ($i = 0; $i -lt $raw.Length - 1; $i++) {
        if ($raw[$i] -eq 10 -and $raw[$i + 1] -eq 10) { $sep = $i; break }
    }
    if ($sep -lt 0) { throw (L 'Anahtar dosyasının biçimi tanınmadı (başlık bulunamadı).' 'Unrecognised key file format (no header).') }
    $header = [System.Text.Encoding]::ASCII.GetString($raw, 0, $sep).Split("`n")
    if ($header.Count -lt 2 -or $header[0] -ne $Script:EnvelopeMagic -or -not $header[1].StartsWith('scope=')) {
        throw (L 'Anahtar dosyasının biçimi tanınmadı (STOWLINE-DPAPI-1 değil).' 'Unrecognised key file format (not STOWLINE-DPAPI-1).')
    }
    $scopeName = $header[1].Substring('scope='.Length)
    if ($scopeName -eq 'machine') {
        $scope = [System.Security.Cryptography.DataProtectionScope]::LocalMachine
    }
    elseif ($scopeName -eq 'user') {
        $scope = [System.Security.Cryptography.DataProtectionScope]::CurrentUser
    }
    else {
        throw ((L 'Bilinmeyen anahtar kapsamı: ' 'Unknown key scope: ') + $scopeName)
    }
    $blob = New-Object byte[] ($raw.Length - $sep - 2)
    [Array]::Copy($raw, $sep + 2, $blob, 0, $blob.Length)
    try {
        return [System.Security.Cryptography.ProtectedData]::Unprotect($blob, $null, $scope)
    }
    catch {
        throw ((L "Anahtar çözülemedi (bu PC'de ve yönetici olarak mı çalışıyorsunuz?): " 'Could not decrypt the key (are you on this PC and running as administrator?): ') + $_.Exception.Message)
    }
}

# Not the key: a short hash to recognise it by later.
function Get-KeyFingerprint {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $hash = $sha.ComputeHash($Bytes) } finally { $sha.Dispose() }
    $hex = ($hash | ForEach-Object { $_.ToString('X2') }) -join ''
    return ($hex.Substring(0, 12) -replace '(.{4})(?=.)', '$1-')
}

function Test-KeyMatches {
    param(
        [Parameter(Mandatory)][byte[]]$Expected,
        [Parameter(Mandatory)][System.Security.SecureString]$Candidate
    )
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($Candidate)
    try { $text = [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) }
    finally { [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    if ([string]::IsNullOrWhiteSpace($text)) { return $false }
    $want = Get-KeyFingerprint -Bytes $Expected
    # A copy typed from paper in groups has spaces in it; the key itself never does.
    foreach ($variant in @($text.Trim(), ($text -replace '\s', ''))) {
        if ((Get-KeyFingerprint -Bytes ([System.Text.Encoding]::UTF8.GetBytes($variant))) -eq $want) { return $true }
    }
    return $false
}

function Get-ControlPlaneBase {
    param([Parameter(Mandatory)][string]$Dir, [string]$Override)
    $url = $Override
    if (-not $url) {
        $cfgPath = Join-Path (Join-Path (Split-Path -Parent $Dir) 'config') 'pilot.json'
        if (-not (Test-Path -LiteralPath $cfgPath)) { throw ((L 'Yapılandırma dosyası bulunamadı: ' 'Configuration file not found: ') + $cfgPath) }
        $url = [string](Get-Content -LiteralPath $cfgPath -Raw | ConvertFrom-Json).control_plane_url
    }
    if (-not $url) { throw (L 'Kontrol düzlemi adresi bulunamadı (pilot.json control_plane_url).' 'Control plane address not found (pilot.json control_plane_url).') }
    $uri = [System.Uri]$url
    $local = $uri.IsLoopback
    if ($uri.Scheme -ne 'https' -and -not $local) { throw ((L 'Kontrol düzlemi adresi https olmalı: ' 'The control plane address must be https: ') + $url) }
    return $url.TrimEnd('/')
}

# Sends the key to the vault as this PC (its own control credential), then
# checks the server's fingerprint matches the local one. Returns the fingerprint.
function Send-KeyToControlPlane {
    param(
        [Parameter(Mandatory)][byte[]]$KeyBytes,
        [Parameter(Mandatory)][string]$Dir,
        [string]$Override
    )
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $base = Get-ControlPlaneBase -Dir $Dir -Override $Override
    $credPath = Get-EnvelopePath -Dir $Dir -Names @('control-credential.machine.dpapi', 'control-credential.user.dpapi')
    if (-not $credPath) { throw ((L 'Cihaz kimlik bilgisi bulunamadı: ' 'Device credential not found: ') + $Dir) }
    $credBytes = Get-EscrowKeyBytes -Path $credPath
    try {
        $cred = [System.Text.Encoding]::UTF8.GetString($credBytes)
        $body = ConvertTo-Json -InputObject @{ kind = 'restic-password'; secret = [System.Text.Encoding]::UTF8.GetString($KeyBytes) } -Compress
        $resp = Invoke-RestMethod -Method Put -Uri "$base/api/v1/agent/escrow" -Headers @{ Authorization = "Bearer $cred" } -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 30
    }
    finally {
        [Array]::Clear($credBytes, 0, $credBytes.Length)
    }
    $local = Get-KeyFingerprint -Bytes $KeyBytes
    if ($resp.fingerprint -ne $local) { throw ((L "Sunucudaki parmak izi ($($resp.fingerprint)) bu PC'dekiyle ($local) eşleşmiyor." "The server's fingerprint ($($resp.fingerprint)) does not match this PC's ($local).")) }
    return $local
}

function Invoke-UploadStep {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    if ($NoUpload) { return }
    if (-not $Yes) {
        $ans = Read-Host (L 'Anahtarın bir kopyasını sunucudaki Şifre Kasası''na da kaydedeyim mi? (E/H)' 'Also save a copy of the key in the server''s Key Vault? (Y/N)')
        if ($ans -notin @('E', 'e', 'EVET', 'evet', 'Y', 'y', 'YES', 'yes')) { Write-Host (L 'Sunucuya gönderilmedi.' 'Not sent to the server.'); return }
    }
    try {
        $fp = Send-KeyToControlPlane -KeyBytes $Bytes -Dir $SecretsDir -Override $ControlPlaneUrl
        Write-Host ((L "Şifre Kasası'na kaydedildi. Parmak izi: $fp (panelde aynı değeri görmelisiniz)." "Saved to the Key Vault. Fingerprint: $fp (the panel should show the same value).")) -ForegroundColor Green
    }
    catch {
        Write-Host ((L "Şifre Kasası'na kaydedilemedi: " 'Could not save to the Key Vault: ') + $_.Exception.Message) -ForegroundColor Red
        Write-Host (L 'Anahtarı panelde Şifre Kasası sayfasından elle de kaydedebilirsiniz.' 'You can also save the key by hand on the Key Vault page of the panel.') -ForegroundColor Yellow
    }
}

function Clear-ConsoleAndScrollback {
    Clear-Host
    Write-Host ([string][char]27 + '[3J') -NoNewline
}

function Invoke-VerifyLoop {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        $copy = Read-Host -AsSecureString (L 'Kaydettiğiniz kopyayı yapıştırın (yazdıkça görünmez), sonra Enter' 'Paste the copy you saved (it stays hidden as you type), then Enter')
        if (Test-KeyMatches -Expected $Bytes -Candidate $copy) {
            Write-Host (L 'EŞLEŞTİ: kaydettiğiniz kopya bu PC''deki anahtarla birebir aynı.' 'MATCH: your saved copy is identical to the key on this PC.') -ForegroundColor Green
            return $true
        }
        Write-Host (L "EŞLEŞMEDİ (deneme $attempt/3). Kopya eksik, fazla ya da yanlış karakter içeriyor olabilir; büyük/küçük harf de önemlidir." "NO MATCH (attempt $attempt/3). The copy may be missing, have extra or wrong characters; upper/lower case matters too.") -ForegroundColor Yellow
    }
    Write-Host (L 'Kopya doğrulanamadı. Bu kopyaya güvenmeyin: betiği yeniden çalıştırıp anahtarı tekrar kaydedin.' 'The copy could not be verified. Do not trust it: run the script again and save the key again.') -ForegroundColor Red
    return $false
}

function Show-Key {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    $text = [System.Text.Encoding]::UTF8.GetString($Bytes)
    Write-Host ''
    Write-Host (L 'ANAHTAR (parola yöneticisine kopyala/yapıştır):' 'KEY (copy/paste into a password manager):') -ForegroundColor Cyan
    Write-Host "  $text" -ForegroundColor White
    if ($text.Length -ge 32) {
        Write-Host ''
        Write-Host (L 'Kağıda yazmak için (8''erli gruplar, aralarındaki boşluklar anahtarın parçası değildir):' 'For writing on paper (groups of 8; the spaces are not part of the key):') -ForegroundColor Cyan
        $groups = for ($i = 0; $i -lt $text.Length; $i += 8) { $text.Substring($i, [Math]::Min(8, $text.Length - $i)) }
        for ($i = 0; $i -lt $groups.Count; $i += 4) {
            Write-Host ('  ' + (($groups[$i..([Math]::Min($i + 3, $groups.Count - 1))]) -join ' ')) -ForegroundColor White
        }
    }
    Write-Host ''
    Write-Host ("Uzunluk: {0} karakter" -f $text.Length)
}

function Invoke-Main {
    if (-not (Test-IsAdministrator)) {
        Write-Host (L 'Bu betik Yönetici olarak çalışmalı. Export-ResticKeyEscrow.cmd dosyasına çift tıklayın.' 'This script must run as administrator. Double-click Export-ResticKeyEscrow.cmd.') -ForegroundColor Red
        exit 1
    }
    $path = Get-EnvelopePath -Dir $SecretsDir
    if (-not $path) {
        Write-Host (L "Anahtar dosyası bulunamadı: $SecretsDir (restic-password.machine.dpapi). Bu, Stowline kurulu olan PC mi?" "Key file not found: $SecretsDir (restic-password.machine.dpapi). Is Stowline installed on this PC?") -ForegroundColor Red
        exit 1
    }
    $bytes = Get-EscrowKeyBytes -Path $path
    try {
        Write-Host ''
        Write-Host (L '=== Stowline Yedekleme Anahtarı ===' '=== Stowline Backup Key ===') -ForegroundColor Cyan
        Write-Host "Bilgisayar     : $env:COMPUTERNAME"
        Write-Host ((L 'Anahtar dosyası: ' 'Key file: ') + $path)
        Write-Host ((L 'Parmak izi     : {0}   (anahtarın kendisi değil; sonradan tanımak için)' 'Fingerprint    : {0}   (not the key itself; to recognise it later)') -f (Get-KeyFingerprint -Bytes $bytes))
        Write-Host ''
        if ($UploadOnly) {
            Invoke-UploadStep -Bytes $bytes
            return
        }
        if ($Verify) {
            if (-not (Invoke-VerifyLoop -Bytes $bytes)) { exit 2 }
            return
        }
        Write-Host (L 'Bu anahtar, Google Drive''daki şifreli yedeklerin TEK açıcısıdır. Kaybolursa yedekler kalıcı olarak okunamaz.' 'This key is the ONLY way to open the encrypted backups. If it is lost, the backups can never be read.') -ForegroundColor Yellow
        Write-Host (L '  - E-posta, sohbet, bilet veya ekran görüntüsü ile göndermeyin; yalnızca aşağıdaki gibi saklayın.' '  - Never send it by e-mail, chat, ticket or screenshot; only store it as described below.') -ForegroundColor Yellow
        Write-Host (L '  - Betik diske hiçbir şey yazmaz; kurulumu, servisi ve anahtarı değiştirmez.' '  - The script writes nothing to disk and does not change the install, the service or the key.') -ForegroundColor Yellow
        Write-Host ''
        $go = Read-Host (L 'Anahtarı ekranda göstermek için EVET yazın' 'Type YES to show the key on screen')
        if ($go -notin @('EVET', 'YES')) { Write-Host (L 'İptal edildi; anahtar gösterilmedi.' 'Cancelled; the key was not shown.'); return }
        Show-Key -Bytes $bytes
        Write-Host ''
        [void](Read-Host (L 'Anahtarı kaydettiyseniz Enter''a basın (ekran temizlenecek)' 'Press Enter once you have saved the key (the screen will be cleared)'))
        Clear-ConsoleAndScrollback
        Write-Host (L 'Şimdi kaydettiğiniz kopyayı doğrulayalım.' 'Now let us verify the copy you saved.') -ForegroundColor Cyan
        if (-not (Invoke-VerifyLoop -Bytes $bytes)) { exit 2 }
        Write-Host ''
        Invoke-UploadStep -Bytes $bytes
        Write-Host ''
        Write-Host (L 'Saklama kuralı: en az İKİ kopya, FARKLI kişilerde/yerlerde (ör. parola yöneticisi + kasadaki mühürlü zarf).' 'Storage rule: at least TWO copies, with DIFFERENT people/places (e.g. password manager + sealed envelope in a safe).') -ForegroundColor Cyan
        Write-Host (L 'Bu pencereyi kapatın.' 'Close this window.') -ForegroundColor Cyan
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }
