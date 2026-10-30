# windows-e2e.ps1 - kcx end to end in PowerShell, in a home of its own: the build,
# an import, the shell snippet and its prompt hook, a switch, kcx env pwsh, a run,
# kcx doctor, and kcx rm to the Recycle Bin. CI runs it on Windows; it touches
# nothing outside its own temp directory. Run from the repository's root.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Check([bool]$ok, [string]$what) {
  if (-not $ok) { throw "FAILED: $what" }
  Write-Host "ok   $what"
}

$onWindows = [Environment]::OSVersion.Platform -eq 'Win32NT'
$work = Join-Path ([IO.Path]::GetTempPath()) ('kcx-e2e-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
$home2 = Join-Path $work 'home'
New-Item -ItemType Directory -Force -Path $home2 | Out-Null
try {
  $exe = Join-Path $work ($(if ($onWindows) { 'kcx.exe' } else { 'kcx' }))
  go build -o $exe ./cmd/kcx
  Check ($LASTEXITCODE -eq 0) 'go build'
  $env:PATH = $work + [IO.Path]::PathSeparator + $env:PATH
  # On Windows kcx follows kubectl's home: USERPROFILE, with HOME unset.
  foreach ($v in 'HOME', 'HOMEDRIVE', 'HOMEPATH', 'KUBECONFIG', 'KCX_AUTO') { Remove-Item "Env:$v" -ErrorAction Ignore }
  if ($onWindows) { $env:USERPROFILE = $home2 } else { $env:HOME = $home2 }
  $kube = [IO.Path]::Combine($home2, '.kube')

  $kc = Join-Path $work 'demo.yaml'
  Set-Content -Encoding ascii -Path $kc -Value @'
apiVersion: v1
kind: Config
clusters:
- name: demo
  cluster: {server: 'https://127.0.0.1:6443'}
users:
- name: demo
  user: {token: not-a-secret}
contexts:
- name: demo
  context: {cluster: demo, user: demo}
current-context: demo
'@

  Write-Host (kcx -v)
  kcx add demo $kc
  $demo = [IO.Path]::Combine($kube, 'configs', 'demo.yaml')
  Check ($LASTEXITCODE -eq 0 -and (Test-Path -LiteralPath $demo)) "kcx add demo FILE: $demo"

  # The snippet: KUBECONFIG = the state file, then the cluster's file.
  kcx init pwsh | Out-String | Invoke-Expression
  $want = [IO.Path]::Combine($kube, 'config') + [IO.Path]::PathSeparator + $demo
  Check ($env:KUBECONFIG -ceq $want) "kcx init pwsh: KUBECONFIG is $env:KUBECONFIG"
  Check ($env:KCX_AUTO -ceq $want) 'kcx init pwsh: KCX_AUTO tracks it'

  # A second cluster: the next prompt takes it in.
  kcx add demo2 $kc
  prompt | Out-Null
  Check ($env:KUBECONFIG -clike '*demo2.yaml') "the prompt hook took in demo2: $env:KUBECONFIG"

  kcx demo
  Check ($LASTEXITCODE -eq 0) 'kcx demo'
  Check ((kcx -c) -ceq 'demo') 'kcx -c is demo'
  Check ((Get-Content -Raw ([IO.Path]::Combine($kube, 'config'))) -match 'current-context: demo') 'the state file holds the current context'

  $assignments = (kcx env pwsh) -join "`n"
  Check ($assignments -clike '$env:KUBECONFIG = *') 'kcx env pwsh'
  Invoke-Expression $assignments
  Check ($env:KUBECONFIG -ceq $env:KCX_AUTO) 'kcx env pwsh evaluates'

  $out = kcx run demo -- pwsh -NoProfile -Command '$env:KCX_RUN'
  Check ($LASTEXITCODE -eq 0 -and $out -ceq 'demo') "kcx run passes the run's own environment: $out"
  kcx run demo -- pwsh -NoProfile -Command 'exit 7'
  Check ($LASTEXITCODE -eq 7) 'kcx run returns the command''s exit status'

  $doctor = kcx doctor | Out-String
  Write-Host $doctor
  Check ($doctor -notmatch 'readable by others') 'kcx doctor raises no Unix file modes'

  if ($onWindows) {
    kcx rm demo2
    Check ($LASTEXITCODE -eq 0) 'kcx rm demo2 (the Recycle Bin)'
    Check (-not (Test-Path -LiteralPath ([IO.Path]::Combine($kube, 'configs', 'demo2.yaml')))) 'demo2.yaml left ~/.kube'

    # A fetch whose secret provider is a PowerShell script, which kcx runs with
    # Windows PowerShell by its absolute path; the platform's command (here a
    # script too, started by its full command line) reads the password on stdin.
    $bin = Join-Path $work 'bin'
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    $secretScript = Join-Path $bin 'fake_secret.ps1'
    Set-Content -Encoding ascii -Path $secretScript -Value @'
param([string]$verb)
if ($env:FAKE_UNAVAILABLE) { [Console]::Error.WriteLine('fake_secret: this PowerShell will not run me'); exit 3 }
switch ($verb) {
  'get' { $b = [Text.Encoding]::UTF8.GetBytes('hunter2'); $o = [Console]::OpenStandardOutput(); $o.Write($b, 0, $b.Length); $o.Flush(); exit 0 }
  'status' { 'cached'; exit 0 }
  default { exit 0 }
}
'@
    $platform = Join-Path $bin 'fake_platform.ps1'
    Set-Content -Encoding ascii -Path $platform -Value @'
param([string]$out)
$pw = [Console]::In.ReadLine()
if ($pw -cne 'hunter2') { [Console]::Error.WriteLine('401 Unauthorized'); exit 1 }
Copy-Item -LiteralPath $env:FAKE_KUBECONFIG -Destination $out
exit 0
'@
    $winPS = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $profiles = [IO.Path]::Combine($home2, '.config', 'kcx', 'profiles')
    New-Item -ItemType Directory -Force -Path $profiles | Out-Null
    Set-Content -Encoding ascii -Path (Join-Path $profiles 'lab.toml') -Value @"
[provider.lab]
command  = ['$winPS', '-NoProfile', '-NonInteractive', '-File', '$platform', '{out}']
secret   = '$secretScript'
host     = '{name}.example'
user     = 'jane'
rejected = '401'

[group.lab]
provider = 'lab'

[cluster.lab1]
group = 'lab'
"@
    $env:FAKE_KUBECONFIG = $kc
    kcx fetch lab1
    Check ($LASTEXITCODE -eq 0) 'kcx fetch lab1 through a .ps1 secret provider and Windows PowerShell'
    Check (Test-Path -LiteralPath ([IO.Path]::Combine($kube, 'configs', 'lab1.yaml'))) 'lab1.yaml imported'
    $status = (kcx secret status) | Out-String
    Check ($LASTEXITCODE -eq 0 -and $status -match 'cached') "kcx secret status: $status"

    # The provider cannot run here (exit 3), and no one is at a terminal: kcx
    # says so and runs nothing.
    $env:FAKE_UNAVAILABLE = '1'
    $out = (kcx fetch lab1 2>&1) | Out-String
    Check ($LASTEXITCODE -ne 0 -and $out -match 'cannot run here, and there is no terminal') "a provider that cannot run here: $out"
    $status = (kcx secret status) | Out-String
    Check ($status -match 'ask for the secret itself') "kcx secret status of a provider that cannot run here: $status"
    Remove-Item Env:FAKE_UNAVAILABLE
  }
} finally {
  Remove-Item -Recurse -Force -LiteralPath $work -ErrorAction Ignore
}
