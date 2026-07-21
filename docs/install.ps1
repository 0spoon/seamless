# Seamless one-command installer for Windows (PowerShell).
#
#   irm https://thereisnospoon.org/install.ps1 | iex
#
# This file IS the published artifact -- the Windows companion to docs/install
# (the POSIX `curl | sh` path). GitHub Pages serves docs/ verbatim, so this file
# lands at https://thereisnospoon.org/install.ps1 unchanged. It is not generated
# (docsgen owns docs/docs/ only), and there is deliberately no second copy under
# scripts/: a script living at two paths is a script that drifts.
#
# What it does: fetch this platform's release zip from GitHub, verify its
# checksum, install seamlessd.exe + seam.exe into ~\.local\bin, wire the
# detected agent clients -- hooks + MCP + maintained skills for Claude
# Code/Codex, and the MCP bridge for the Claude app chat surface (it has no
# hooks or skills) -- generating the bearer key on first run, then run the
# daemon as a per-user Scheduled Task
# -- an at-logon task running as you, no admin. Codex gets the secret-preserving
# stdio proxy by default; direct HTTP remains a supported manual Codex
# configuration. That is
# the Windows analog of launchd / systemd --user: the whole install is per-user,
# which is why it never elevates. Re-running it upgrades in place; the config and
# ~\.seamless are never touched. Uninstall anytime with `seamlessd.exe uninstall`
# (add --purge to also delete the config and data).
#
# Overrides (set as environment variables before running):
#   $env:SEAMLESS_VERSION             version to install (default: latest release)
#   $env:SEAMLESS_INSTALL_DIR         where the binaries go (default: ~\.local\bin)
#   $env:SEAMLESS_CLIENT              claude|codex|claude-desktop|all, or a comma
#                                     list of targets (default: the detected
#                                     clients; prompts when several or none are
#                                     found, and aborts when none is found and
#                                     the session is non-interactive -- never a
#                                     silent default)
#   $env:SEAMLESS_NO_HOOKS=1          skip agent hooks, MCP registration, and skills
#   $env:SEAMLESS_NO_ONBOARD_SKILL=1  skip the one-shot seam-onboard skill
#   $env:SEAMLESS_NO_RESEARCH_SKILL=1 skip the recurring seam-research skill
#   $env:SEAMLESS_NO_SERVICE=1        skip the service; install the binaries and stop

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # no per-request progress bar spam

# Windows PowerShell 5.1 does not negotiate TLS 1.2 by default, and GitHub
# requires it. Harmless on PowerShell 7. Best-effort: some hosts lock it down.
try {
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

$Repo = '0spoon/seamless'
$TaskName = 'Seamless'
$DocsUrl = 'https://thereisnospoon.org/docs/'

# Canonical paths, kept identical to the POSIX installer so the config the daemon
# searches, the task runs against, and the hooks resolve are one file. On Windows
# $HOME is %USERPROFILE%, so ~\.config\seamless is exactly what the Go config
# search path (os.UserHomeDir + .config\seamless) already looks for.
$ConfigDir = Join-Path $HOME '.config\seamless'
$Config = Join-Path $ConfigDir 'seamless.yaml'
$DataDir = Join-Path $HOME '.seamless'
$LogFile = Join-Path $DataDir 'seamlessd.log'

# Pre-config fallback for the health poll only; mirrors config.Defaults().
$DefaultAddr = '127.0.0.1:8081'

function Say { param([string]$Message) Write-Host "  $Message" }
function Step { param([string]$Key, [string]$Message) Write-Host ('  {0,-12} {1}' -f $Key, $Message) }
function Warn { param([string]$Message) Write-Warning $Message }
function Die { param([string]$Message) [Console]::Error.WriteLine("`nerror: $Message"); exit 1 }

# Target selection, kept in step with docs/install and the interactive
# `seamlessd install-hooks` menu: the two hook clients plus the Claude app chat
# surface (MCP bridge only, no hooks or skills). Unlike the POSIX curl|sh pipe,
# `irm | iex` runs in the caller's session, so stdin is still the console and
# Read-Host works: with several targets detected the installer confirms which
# to wire (default: the detected set, multi-select via a comma list); with none
# it asks whether to install at all (default no), then which target (no
# default). Non-interactively, several resolves to the detected set and none
# aborts with guidance -- there is deliberately no silent Claude Code fallback.
# The resolved comma list is passed explicitly to install-hooks, which installs
# the matching hooks, MCP registrations, and skill packages together.
function Test-Interactive {
    return ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected)
}

# ConvertTo-ClientList: map an answer -- numbers or target words, singly or
# comma-separated -- to the canonical comma list (claude, codex, claude-desktop
# in stable order). $null for any unrecognized token, so callers can re-prompt
# or die instead of guessing.
function ConvertTo-ClientList {
    param([string]$Raw)
    $claude = $false; $codex = $false; $desktop = $false; $any = $false
    foreach ($tok in ($Raw -split '[,\s]+')) {
        $t = $tok.ToLowerInvariant()
        if ($t -eq '') { continue }
        switch ($t) {
            { $_ -in '1', 'claude', 'claude-code', 'cc' } { $claude = $true; break }
            { $_ -in '2', 'codex', 'cx' } { $codex = $true; break }
            { $_ -in '3', 'claude-desktop', 'desktop' } { $desktop = $true; break }
            { $_ -in '4', 'all' } { $claude = $true; $codex = $true; $desktop = $true; break }
            'both' { $claude = $true; $codex = $true; break }
            default { return $null }
        }
        $any = $true
    }
    if (-not $any) { return $null }
    $targets = @()
    if ($claude) { $targets += 'claude' }
    if ($codex) { $targets += 'codex' }
    if ($desktop) { $targets += 'claude-desktop' }
    return ($targets -join ',')
}

function Test-ClientHas {
    param([string]$List, [string]$Target)
    return (($List -split ',') -contains $Target)
}

function Read-ClientChoice {
    param([string]$DefaultChoice) # '' = no default: an explicit answer is required
    # Keep the entry labels in step with install_hooks.go and docs/install (a
    # Go test pins them).
    Write-Host 'Wire Seamless to which agent client?'
    Write-Host ('  [1] Claude Code {0}' -f $(if ($script:ClaudeDetected) { '(detected)' } else { '(not detected)' }))
    Write-Host ('  [2] Codex (app/CLI/IDE) {0}' -f $(if ($script:CodexDetected) { '(detected)' } else { '(not detected)' }))
    Write-Host ('  [3] Claude app (chat) {0}' -f $(if ($script:DesktopDetected) { '(detected)' } else { '(not detected)' }))
    Write-Host '  [4] All'
    while ($true) {
        $prompt = if ($DefaultChoice) { "Enter choices like 1 or 1,3 [$DefaultChoice]" } else { 'Enter choices like 1 or 1,3' }
        $answer = Read-Host $prompt
        if (-not $answer) { $answer = $DefaultChoice }
        $list = ConvertTo-ClientList $answer
        if ($list) { return $list }
        Write-Host '  please enter numbers 1-4, comma-separated for several'
    }
}

function Resolve-AgentClient {
    if ($env:SEAMLESS_CLIENT) {
        $list = ConvertTo-ClientList $env:SEAMLESS_CLIENT
        if (-not $list) { Die "invalid SEAMLESS_CLIENT=$env:SEAMLESS_CLIENT (valid values: claude, codex, claude-desktop, all, or a comma list)" }
        return $list
    }
    $claude = (((Get-Command claude -ErrorAction SilentlyContinue) -ne $null) -or (Test-Path (Join-Path $HOME '.claude')))
    $codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $HOME '.codex' }
    $codex = (((Get-Command codex -ErrorAction SilentlyContinue) -ne $null) -or (Test-Path $codexHome))
    # The chat surface: an existing desktop config is the only portable signal
    # on Windows (no documented app-install path to guess) -- the same gating
    # seamlessd's claudeDesktopSurfaceDetected uses.
    $desktop = ($env:APPDATA -and (Test-Path (Join-Path $env:APPDATA 'Claude\claude_desktop_config.json')))
    $script:ClaudeDetected = $claude
    $script:CodexDetected = $codex
    $script:DesktopDetected = $desktop
    $targets = @()
    if ($claude) { $targets += 'claude' }
    if ($codex) { $targets += 'codex' }
    if ($desktop) { $targets += 'claude-desktop' }
    if ($targets.Count -eq 1) { return $targets[0] }
    if ($targets.Count -gt 1) {
        $detected = $targets -join ','
        if (-not (Test-Interactive)) { return $detected }
        # The menu default is the detected set as numbers, collapsed to 4 (All)
        # when every target was detected.
        $default = if ($targets.Count -eq 3) { '4' } else {
            (($targets | ForEach-Object { @{ 'claude' = '1'; 'codex' = '2'; 'claude-desktop' = '3' }[$_] }) -join ',')
        }
        return Read-ClientChoice $default
    }
    if (-not (Test-Interactive)) {
        Die 'no agent client was detected on this machine (Claude Code, Codex, or the Claude app); set $env:SEAMLESS_CLIENT=claude|codex|claude-desktop|all to install anyway'
    }
    Warn 'no agent client was detected on this machine (Claude Code, Codex, or the Claude app)'
    $answer = Read-Host 'Install anyway? [y/N]'
    if ($answer -notmatch '^(y|yes)$') {
        Die 'aborted: no agent client detected (set $env:SEAMLESS_CLIENT=claude|codex|claude-desktop|all to force)'
    }
    return Read-ClientChoice ''
}

# Map the process architecture onto goreleaser's vocabulary. PROCESSOR_ARCHITEW6432
# is set when a 32-bit PowerShell runs on 64-bit Windows (WOW64); it names the real
# machine, so it wins over PROCESSOR_ARCHITECTURE.
function Get-Arch {
    $a = $env:PROCESSOR_ARCHITEW6432
    if (-not $a) { $a = $env:PROCESSOR_ARCHITECTURE }
    switch ($a) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { Die "unsupported architecture: $a (Seamless ships amd64 and arm64 builds)" }
    }
}

# The GitHub API rather than parsing the /releases/latest redirect: a rate-limited
# API answers with JSON we can detect instead of a 302 we would silently misread.
function Resolve-Version {
    if ($env:SEAMLESS_VERSION) { return ($env:SEAMLESS_VERSION -replace '^v', '') }
    $tag = $null
    try {
        $rel = Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$Repo/releases/latest"
        $tag = $rel.tag_name
    } catch { $tag = $null }
    if (-not $tag) {
        Die @"
could not resolve the latest release (GitHub API rate limit?); pin one instead:
  `$env:SEAMLESS_VERSION='0.3.0'; irm https://thereisnospoon.org/install.ps1 | iex
"@
    }
    return ($tag -replace '^v', '')
}

# Download the zip and checksums.txt, and verify the SHA-256 before unpacking
# anything. Match the filename exactly in checksums.txt -- a substring match would
# happily verify amd64 against arm64.
function Get-Release {
    param([string]$Version, [string]$Arch, [string]$Tmp)
    $base = "https://github.com/$Repo/releases/download/v$Version"
    $zip = "seamless_${Version}_windows_${Arch}.zip"
    $zipPath = Join-Path $Tmp $zip
    $sumPath = Join-Path $Tmp 'checksums.txt'

    Step 'downloading' $zip
    try { Invoke-WebRequest -UseBasicParsing "$base/$zip" -OutFile $zipPath }
    catch { Die "no such release asset: $base/$zip`ncheck the version at https://github.com/$Repo/releases" }
    try { Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile $sumPath }
    catch { Die "could not fetch $base/checksums.txt" }

    $want = Get-Content $sumPath |
        Where-Object { ($_ -split '\s+')[1] -eq $zip } |
        ForEach-Object { ($_ -split '\s+')[0] } |
        Select-Object -First 1
    if (-not $want) { Die "$zip is not listed in checksums.txt" }
    $got = (Get-FileHash -Algorithm SHA256 $zipPath).Hash
    if ($want.ToLower() -ne $got.ToLower()) {
        Die "checksum mismatch for $zip`n  expected $want`n  got      $got"
    }
    Step 'checksum' 'ok'

    Test-Signature -Base $base -Tmp $Tmp -SumPath $sumPath
    return $zipPath
}

# A matching checksum proves the zip is the one checksums.txt describes, not that
# checksums.txt came from us -- it is fetched from the same origin, so whoever
# could tamper with one could tamper with both. The signature closes that gap
# (audit M3); mirrors verify_signature in docs/install.
#
# Keyless Sigstore, so the IDENTITY is the check that matters: anyone can produce
# a valid signature under their own identity, and pinning to this repo's release
# workflow on a v* tag is what makes it mean "published by the Seamless
# pipeline". cosign missing warns (installing a signing tool first would be real
# friction, and a first install is trust-on-first-use over TLS regardless);
# cosign present and failing is fatal, because that is positive evidence.
function Test-Signature {
    param([string]$Base, [string]$Tmp, [string]$SumPath)

    if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
        Warn "cosign not found -- archive verified by checksum only.`n    For signature verification: https://docs.sigstore.dev/system_config/installation/"
        return
    }
    $sig = Join-Path $Tmp 'checksums.txt.sig'
    $cert = Join-Path $Tmp 'checksums.txt.pem'
    try {
        Invoke-WebRequest -UseBasicParsing "$Base/checksums.txt.sig" -OutFile $sig
        Invoke-WebRequest -UseBasicParsing "$Base/checksums.txt.pem" -OutFile $cert
    } catch {
        Warn 'this release predates artifact signing -- checksum only'
        return
    }
    $idRegex = "^https://github.com/$Repo/\.github/workflows/release\.yml@refs/tags/v"
    & cosign verify-blob $SumPath `
        --signature $sig `
        --certificate $cert `
        --certificate-identity-regexp $idRegex `
        --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Die "SIGNATURE VERIFICATION FAILED for checksums.txt.`n  The release artifacts are not signed by the $Repo release workflow.`n  Do not install. Please report this at https://github.com/$Repo/security"
    }
    Step 'signature' 'ok (sigstore)'
}

# A running .exe cannot be overwritten in place -- the image is locked -- and
# re-running this script over a live install IS the upgrade path. Windows does
# allow *renaming* a loaded exe, so move the old one aside first, then drop the
# new one into the freed name. The stale copy is removed best-effort (it may still
# be locked for a moment); the next run clears any leftover.
function Install-OneBinary {
    param([string]$Src, [string]$Dst)
    if (Test-Path $Dst) {
        $old = "$Dst.old"
        Remove-Item -Force $old -ErrorAction SilentlyContinue
        try { Rename-Item -Path $Dst -NewName ([IO.Path]::GetFileName($old)) -Force } catch {}
    }
    Copy-Item -Path $Src -Destination $Dst -Force
    Remove-Item -Force "$Dst.old" -ErrorAction SilentlyContinue
}

function Install-Binaries {
    param([string]$ZipPath, [string]$Tmp, [string]$InstallDir)
    Expand-Archive -Path $ZipPath -DestinationPath $Tmp -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Install-OneBinary (Join-Path $Tmp 'seamlessd.exe') (Join-Path $InstallDir 'seamlessd.exe')
    Install-OneBinary (Join-Path $Tmp 'seam.exe') (Join-Path $InstallDir 'seam.exe')
    Step 'installed' "$InstallDir\seamlessd.exe, $InstallDir\seam.exe"
}

# install-hooks does the config bootstrap too: it calls config.EnsureAPIKey, which
# generates the bearer key into $Config when no config file exists. So it must run
# BEFORE the service starts. The Push-Location is not decoration: ./seamless.yaml is
# the last entry in the config search path, so running from a dir that had one would
# otherwise bind the install to it. Missing claude/seam is a warning inside
# install-hooks, not a failure, so a box without Claude Code still installs cleanly.
function Invoke-WireHooks {
    param([string]$Tmp, [string]$InstallDir, [string]$AgentClient, [string]$Version)
    if ($env:SEAMLESS_NO_HOOKS) { Step 'hooks' 'skipped (SEAMLESS_NO_HOOKS)'; return }
    $seamlessd = Join-Path $InstallDir 'seamlessd.exe'
    $seam = Join-Path $InstallDir 'seam.exe'
    Push-Location $Tmp
    try {
        if (Test-Path $Config) {
            $env:SEAMLESS_CONFIG = $Config
        } else {
            # Unset, not pointed at $Config: the file does not exist yet, and
            # EnsureAPIKey only writes it when the config resolves to nothing.
            Remove-Item Env:SEAMLESS_CONFIG -ErrorAction SilentlyContinue
        }
        # --client first shipped in v0.3.3, but this script is always fetched from
        # main and can install any pinned $env:SEAMLESS_VERSION. Ask the binary we
        # just unpacked: an unknown flag fails flag parsing, and Die would abort the
        # install before the scheduled task is ever registered. -h parses flags and
        # returns before any config load, so the probe is side-effect free.
        # 2>&1 on a native command can surface as a terminating NativeCommandError
        # under $ErrorActionPreference='Stop', so the probe relaxes it and catches.
        $probe = ''
        $prevEap = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            $probe = (& $seamlessd install-hooks -h 2>&1 | Out-String)
        } catch {
            $probe = ''
        } finally {
            $ErrorActionPreference = $prevEap
        }
        # The claude-desktop target and comma-separated --client lists shipped
        # in the same release, so one probe covers both. A pinned older binary:
        # drop the desktop target visibly (the closing advice keys off
        # $script:WiredClient, so it must not then mention the app) and fall
        # back to that binary's own vocabulary for what remains.
        if ($probe -and $probe -notmatch 'claude-desktop' -and (Test-ClientHas $AgentClient 'claude-desktop')) {
            Warn "seamless $Version predates the Claude app chat surface; skipping claude-desktop (drop SEAMLESS_VERSION to get the latest)"
            $AgentClient = ((($AgentClient -split ',') | Where-Object { $_ -ne 'claude-desktop' }) -join ',')
            if (-not $AgentClient) {
                Die "seamless $Version cannot wire the Claude app chat surface; rerun with `$env:SEAMLESS_CLIENT='claude|codex|all', or drop SEAMLESS_VERSION to get the latest"
            }
        }
        $script:WiredClient = $AgentClient
        $clientValue = $AgentClient
        if ($probe -and $probe -notmatch 'claude-desktop' -and $clientValue -eq 'claude,codex') {
            # Pre-list binaries only understand single words and "all".
            $clientValue = 'all'
        }
        $clientArgs = @('--client', $clientValue)
        # An empty probe failed to run; it did not prove the flag is absent. Assume
        # the modern binary rather than silently downgrading a current install.
        if ($probe -and $probe -notmatch '-client') {
            if ($AgentClient -ne 'claude') {
                Die "seamless $Version predates --client and cannot wire $AgentClient; rerun with `$env:SEAMLESS_CLIENT='claude', or drop SEAMLESS_VERSION to get the latest"
            }
            # Pre-0.3.3 is Claude-Code-only and bundles no skills; the old installer
            # passed no --client here, so this is byte-for-byte its behavior.
            Warn "seamless $Version predates --client and the bundled skills; wiring Claude Code only"
            $clientArgs = @()
        }
        # The embedded skills shipped together with the --skills flag: a pinned
        # v0.3.3-v0.3.6 binary accepts --client but bundles no skills, so the
        # closing seam-onboard advice would name a skill that was never installed.
        if ($probe -and $probe -match '-client' -and $probe -notmatch '-skills') {
            Warn "seamless $Version predates the bundled skills; the seam-onboard/seam-research skills will not be installed (drop SEAMLESS_VERSION to get the latest)"
        }
        & $seamlessd install-hooks @clientArgs --seam $seam
        if ($LASTEXITCODE -ne 0) { Die "install-hooks failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
        Remove-Item Env:SEAMLESS_CONFIG -ErrorAction SilentlyContinue
    }
}

# Read the port back out of the config the way the Makefile and the sh installer
# do, so someone who edited addr: gets a health poll and a console URL that follow
# their change instead of an install that claims to have failed.
function Get-ConfiguredAddr {
    if (-not (Test-Path $Config)) { return $DefaultAddr }
    $m = Select-String -Path $Config -Pattern '^\s*addr:\s*"?([^"\s]+)' | Select-Object -First 1
    if ($m) { return $m.Matches[0].Groups[1].Value }
    return $DefaultAddr
}

# The Windows service: a per-user, at-logon Scheduled Task -- the analog of a
# launchd LaunchAgent / systemd --user unit. LogonType Interactive + RunLevel
# Limited means no admin and no stored password: it runs as you, while you are
# logged in. A task action is a bare exec, not a shell string, so --config and
# --log-file carry what a plist/unit would put in an env prefix: the config to
# pin and the log file the task's process would otherwise write nowhere.
function Register-Service {
    param([string]$InstallDir)
    $seamlessd = Join-Path $InstallDir 'seamlessd.exe'
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

    $arg = 'serve --config "{0}" --log-file "{1}"' -f $Config, $LogFile
    $action = New-ScheduledTaskAction -Execute $seamlessd -Argument $arg
    # The canonical account identity (COMPUTERNAME\user, or DOMAIN\user on a
    # domain-joined box). $env:USERDOMAIN can read back as "WORKGROUP" on a local
    # account, which is not a valid principal, so ask Windows for the real name.
    $userId = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    # -User is load-bearing: without it -AtLogOn means "any user logs on", and
    # registering an any-user logon trigger needs elevation (0x80070005 for a
    # standard user) -- the same rule that makes `schtasks /create /sc onlogon`
    # demand an admin prompt. Scoped to yourself it registers without admin.
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $userId
    $principal = New-ScheduledTaskPrincipal -UserId $userId `
        -LogonType Interactive -RunLevel Limited
    # ExecutionTimeLimit 0 = never auto-kill a long-running daemon (the default is
    # three days); restart-on-failure is the KeepAlive / Restart=always analog;
    # IgnoreNew stops a second copy if the trigger somehow double-fires.
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartInterval (New-TimeSpan -Minutes 1) -RestartCount 3 -MultipleInstances IgnoreNew

    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $TaskName
    Step 'service' "$TaskName (Scheduled Task, at logon)"
}

# The Scheduled Task reports success as soon as it has started the process, but the
# daemon binds its listener ~100ms later. Poll until it actually answers, so a green
# install means it is serving rather than racing a listener that is not up.
function Wait-Healthy {
    param([string]$Addr)
    for ($i = 0; $i -lt 50; $i++) {
        try {
            $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 1 "http://$Addr/healthz"
            if ([int]$r.StatusCode -eq 200) { Step 'healthz' "ok -- http://$Addr"; return }
        } catch {}
        Start-Sleep -Milliseconds 200
    }
    Die "no /healthz from $Addr after 10s; check the log: $LogFile"
}

function Main {
    $InstallDir = if ($env:SEAMLESS_INSTALL_DIR) { $env:SEAMLESS_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }
    $agentClient = Resolve-AgentClient
    # What actually got wired; Invoke-WireHooks narrows it when a pinned old
    # binary cannot carry the chat surface, so the closing advice stays honest.
    $script:WiredClient = $agentClient

    $arch = Get-Arch
    $version = Resolve-Version
    Write-Host ''
    Write-Host ('  seamless {0}  windows/{1}' -f $version, $arch)

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('seamless-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    try {
        $zip = Get-Release $version $arch $tmp

        # Stop a running task before swapping binaries: a live seamlessd.exe holds
        # its own image locked. Best-effort -- absent on a first install.
        if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
            Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        }

        Install-Binaries $zip $tmp $InstallDir
        Invoke-WireHooks $tmp $InstallDir $agentClient $version
        $addr = Get-ConfiguredAddr

        if ($env:SEAMLESS_NO_SERVICE) {
            Step 'service' 'skipped (SEAMLESS_NO_SERVICE)'
            Say "start it yourself: & `"$InstallDir\seamlessd.exe`" serve --config `"$Config`""
        } else {
            Register-Service $InstallDir
            Wait-Healthy $addr
        }
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    # Put the install dir on the user PATH so `seam` works in new shells. The
    # service uses absolute paths, so this is only for interactive use; it takes
    # effect in the next terminal, not this one.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $InstallDir) {
        $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host ''
        Say "added $InstallDir to your user PATH (open a new terminal to pick it up)"
    }

    Write-Host ''
    Say 'next:'
    $wired = $script:WiredClient
    $hasClaude = Test-ClientHas $wired 'claude'
    $hasCodex = Test-ClientHas $wired 'codex'
    $hasDesktop = Test-ClientHas $wired 'claude-desktop'
    if ($hasClaude -and $hasCodex) { Say '  open any git repo in Claude Code or the Codex app, CLI, or IDE -- Seamless maps it to a project on its own' }
    elseif ($hasClaude) { Say '  open any git repo in Claude Code -- Seamless maps it to a project on its own' }
    elseif ($hasCodex) { Say '  open any git repo in the Codex app, CLI, or IDE -- Seamless maps it to a project on its own' }
    if ($hasDesktop) { Say '  chat in the Claude app -- the seamless MCP tools load after an app restart' }
    Say "  & `"$InstallDir\seamlessd.exe`" console-open   # open the console, already logged in"
    Write-Host ''
    if ($hasClaude -and $hasCodex) {
        Say 'restart both clients. Codex CLI users: review/approve Seamless in  /hooks.'
        Say 'Codex desktop app users: hook trust is beta; confirm a <seam-briefing>.'
        Say 'then run  /seam-onboard  in Claude Code or  $seam-onboard  in Codex.'
    } elseif ($hasClaude) {
        Say 'restart Claude Code, then run  /seam-onboard  once.'
    } elseif ($hasCodex) {
        Say 'restart Codex. CLI users: review/approve Seamless in  /hooks.'
        Say 'desktop app users: hook trust is beta; confirm a <seam-briefing>, then run  $seam-onboard  once.'
    }
    if ($hasDesktop) {
        Say 'restart the Claude app to load the chat-surface MCP bridge (it reads its config at startup).'
    }
    Write-Host ''
    Say "uninstall anytime: & `"$InstallDir\seamlessd.exe`" uninstall"
    Say "docs: $DocsUrl"
    Write-Host ''
}

Main
