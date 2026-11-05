# Installs or updates ephget. Run it again to update.
#
#   irm https://github.com/Dryd33n/ephget/releases/latest/download/install.ps1 | iex
#
# It needs no admin rights, and only touches the current user's files and settings.
# These environment variables are for testing:
#   EPHGET_HOME            install here, and leave the real PATH and scheduled task alone
#   EPHGET_INSTALL_SOURCE  a folder to take the files from, instead of the latest release
#   EPHGET_INSTALL_YES     use the default answers instead of asking

# A script block, because exit inside `iex` would close the user's terminal.
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue' # it slows downloads badly in Windows PowerShell
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $releaseUrl = 'https://github.com/Dryd33n/ephget/releases/latest/download'
    $programs = 'ephget.exe', 'ephgetw.exe'
    $sums = 'SHA256SUMS.txt'
    $testing = [bool]$env:EPHGET_HOME
    $dataDir = if ($testing) { $env:EPHGET_HOME } else { Join-Path $env:LOCALAPPDATA 'ephget' }
    $configFile = Join-Path $dataDir 'config.json'
    $isUpdate = Test-Path $configFile
    $staging = Join-Path ([IO.Path]::GetTempPath()) ("ephget-install-" + [guid]::NewGuid())

    New-Item -ItemType Directory -Force $staging | Out-Null
    try {
        # Get the files and check them before anything already installed is touched.
        foreach ($name in $programs + $sums) {
            $target = Join-Path $staging $name
            if ($env:EPHGET_INSTALL_SOURCE) {
                Copy-Item (Join-Path $env:EPHGET_INSTALL_SOURCE $name) $target
            } else {
                Write-Host "Downloading $name"
                Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/$name" -OutFile $target
            }
        }
        $expected = @{}
        foreach ($line in Get-Content (Join-Path $staging $sums)) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2) { $expected[$parts[1].TrimStart('*')] = $parts[0] }
        }
        foreach ($name in $programs) {
            $actual = (Get-FileHash (Join-Path $staging $name) -Algorithm SHA256).Hash
            if (-not $expected.ContainsKey($name) -or $actual -ne $expected[$name]) {
                throw "$name failed verification, so nothing was installed. Try again, and report it if this keeps happening."
            }
        }

        # Only a first install asks. An update keeps the existing settings.
        $configArgs = @()
        if (-not $isUpdate -and -not $env:EPHGET_INSTALL_YES) {
            Write-Host ''
            Write-Host 'ephget puts its shortcuts in these places:'
            Write-Host "  $([Environment]::GetFolderPath('Desktop'))"
            Write-Host '  the Start Menu, in an ephget folder'
            $folder = Read-Host 'Press Enter to use the Desktop, or paste a different folder'
            if ($folder) { $configArgs += '--shortcut-dir', $folder.Trim().Trim('"') }
            $startMenu = Read-Host 'Add shortcuts to the Start Menu too? [Y/n]'
            if ($startMenu -match '^(n|no)$') { $configArgs += '--no-start-menu' }
        }

        New-Item -ItemType Directory -Force $dataDir | Out-Null
        # A waiting launcher holds its exe open. Windows won't replace a running exe but
        # will rename one, so the old copy steps aside and goes once it's free.
        Get-ChildItem $dataDir -Filter 'ephget*.exe.old-*' -ErrorAction SilentlyContinue |
            Remove-Item -Force -ErrorAction SilentlyContinue
        foreach ($name in $programs) {
            $target = Join-Path $dataDir $name
            if (Test-Path $target) {
                Rename-Item $target "$name.old-$([guid]::NewGuid().ToString('N'))"
            }
            Copy-Item (Join-Path $staging $name) $target
            Unblock-File $target
        }

        # Any ephget command registers the scheduled task, and this one saves the settings.
        $ephget = Join-Path $dataDir 'ephget.exe'
        & $ephget config @configArgs | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "ephget couldn't save its settings (exit code $LASTEXITCODE)." }

        if (-not $testing) {
            # Read and written raw, because the usual API would expand any %VARIABLES%
            # in the user's PATH for good.
            $environment = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
            try {
                $path = $environment.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                $present = ($path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $dataDir.TrimEnd('\') }
                if (-not $present) {
                    $environment.SetValue('Path', ($path.TrimEnd(';') + ';' + $dataDir).TrimStart(';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
                    # This call's side effect tells running programs that PATH changed.
                    [Environment]::SetEnvironmentVariable('EPHGET_PATH_REFRESH', $null, 'User')
                    $env:Path = "$env:Path;$dataDir"
                }
            } finally {
                $environment.Close()
            }
        }

        $version = & $ephget version
        Write-Host ''
        if ($isUpdate) {
            Write-Host "Updated to $version."
        } else {
            Write-Host "Installed $version."
            Write-Host 'Find an app with: ephget search <name>'
            Write-Host 'Add it with:      ephget add <id>'
            Write-Host 'If ephget is not found, open a new terminal.'
        }
    } finally {
        Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
    }
}
