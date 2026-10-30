// Package shell holds what kcx prints for a shell to evaluate: the KUBECONFIG
// rule in pure shell (no kcx run per shell start), the guard for a read-only
// shell or a run, a hook that recomputes the variable before each prompt, and
// the completion that calls kcx's hidden word list.
//
// The names KCX_AUTO, KCX_RO and KCX_RUN and the function _kcx_kubeconfig_env
// are a contract with the dotfiles that already carry the zsh version inline.
// KCX_SHELL, KCX_SHELL_AUTO, KCX_RO_CONFIG and KCX_RUN_CONFIG are read here only:
// an inline copy that knows none of them leaves a kcx shell's value alone,
// because that value is tracked by KCX_SHELL_AUTO, not KCX_AUTO.
package shell

import (
	"fmt"
	"strings"
)

// Init returns the snippet for `eval "$(kcx init SHELL)"`, or for PowerShell's
// `kcx init pwsh | Out-String | Invoke-Expression`. kube is the ~/.kube
// directory kcx uses; the PowerShell snippet carries it, since PowerShell's
// $HOME need not be the home kubectl uses on Windows.
func Init(sh, kube string) (string, error) {
	switch sh {
	case "zsh":
		return zshInit + zshCompletion, nil
	case "bash":
		return bashInit + bashCompletion, nil
	case "pwsh", "powershell":
		return "$global:_kcx_kube = " + PwshQuote(kube) + "\n" + pwshInit + pwshCompletion, nil
	}
	return "", fmt.Errorf("kcx init zsh|bash|pwsh: %q is not a shell kcx knows", sh)
}

// Completion returns only the completion part, for shells that already carry
// the KUBECONFIG rule elsewhere (the dotfiles' ~/.zshenv).
func Completion(sh string) (string, error) {
	switch sh {
	case "zsh":
		return zshCompletion, nil
	case "bash":
		return bashCompletion, nil
	case "pwsh", "powershell":
		return pwshCompletion, nil
	}
	return "", fmt.Errorf("kcx completion zsh|bash|pwsh: %q is not a shell kcx knows", sh)
}

// PwshQuote is s as a PowerShell literal: single quotes, each one inside doubled.
func PwshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

const zshInit = `# kcx: KUBECONFIG = the state file, then one file per cluster, kept current in every
# shell. A KUBECONFIG set on purpose is left alone: only a value this function set
# itself (KCX_AUTO) is replaced, and never inside a kcx read-only shell or a kcx run.
# Inside a kcx shell (KCX_SHELL) the shell's private file comes first, and the
# value is tracked by KCX_SHELL_AUTO instead.
_kcx_kubeconfig_env() {
  emulate -L zsh
  [[ -n ${KCX_RO-} || -n ${KCX_RUN-} ]] && return 0
  local LC_ALL=C
  local -a files
  files=( ~/.kube/config ~/.kube/configs/*.yaml(N.on) ~/.kube/temp/*.yaml(N.on) )
  if [[ -n ${KCX_SHELL-} ]]; then
    [[ -z ${KUBECONFIG-} || $KUBECONFIG == ${KCX_SHELL_AUTO-} ]] || return 0
    files=( $KCX_SHELL $files )
    export KUBECONFIG=${(j.:.)files} KCX_SHELL_AUTO=${(j.:.)files}
    return 0
  fi
  [[ -z ${KUBECONFIG-} || $KUBECONFIG == ${KCX_AUTO-} ]] || return 0
  export KUBECONFIG=${(j.:.)files} KCX_AUTO=${(j.:.)files}
}
# Inside a kcx shell, a read-only shell or a run, a startup file read after this
# snippet (another tool's) may have set KUBECONFIG; the first prompt puts the
# shell's own back, before anything typed runs.
_kcx_restore_once() {
  emulate -L zsh
  [[ -n ${_kcx_restored-} ]] && return 0
  typeset -g _kcx_restored=1
  precmd_functions=(${precmd_functions:#_kcx_restore_once})
  local LC_ALL=C
  local -a files
  files=( ~/.kube/config ~/.kube/configs/*.yaml(N.on) ~/.kube/temp/*.yaml(N.on) )
  if [[ -n ${KCX_RO_CONFIG-} ]]; then
    export KUBECONFIG=$KCX_RO_CONFIG
  elif [[ -n ${KCX_SHELL-} ]]; then
    files=( $KCX_SHELL $files )
    export KUBECONFIG=${(j.:.)files} KCX_SHELL_AUTO=${(j.:.)files}
  elif [[ -n ${KCX_RUN_CONFIG-} ]]; then
    files=( $KCX_RUN_CONFIG $files )
    export KUBECONFIG=${(j.:.)files}
  fi
}
_kcx_kubeconfig_env
if [[ -o interactive ]]; then
  typeset -ga precmd_functions
  # First in line, so the prompt (Powerlevel10k's included) shows what is current.
  (( ${precmd_functions[(Ie)_kcx_kubeconfig_env]} )) || precmd_functions=(_kcx_kubeconfig_env $precmd_functions)
  if [[ -n ${KCX_RO_CONFIG-}${KCX_SHELL-}${KCX_RUN_CONFIG-} ]]; then
    (( ${precmd_functions[(Ie)_kcx_restore_once]} )) || precmd_functions=(_kcx_restore_once $precmd_functions)
  fi
fi
`

const zshCompletion = `# kcx completes its contexts, clusters and commands from its own word list.
if [[ -o interactive ]]; then
  _kcx() { local -a w; w=(${(f)"$(kcx __complete ${words[2,CURRENT-1]} 2>/dev/null)"}); compadd -a w }
  if (( $+functions[compdef] )); then
    compdef _kcx kcx
  else
    # compinit has not run yet: register at the first prompt, then step aside.
    _kcx_register_completion() {
      (( $+functions[compdef] )) || return 0
      compdef _kcx kcx
      precmd_functions=(${precmd_functions:#_kcx_register_completion})
    }
    typeset -ga precmd_functions
    precmd_functions+=(_kcx_register_completion)
  fi
fi
`

const bashInit = `# kcx: KUBECONFIG = the state file, then one file per cluster, kept current in every
# shell. A KUBECONFIG set on purpose is left alone: only a value this function set
# itself (KCX_AUTO) is replaced, and never inside a kcx read-only shell or a kcx run.
# Inside a kcx shell (KCX_SHELL) the shell's private file comes first, and the
# value is tracked by KCX_SHELL_AUTO instead.
_kcx_kubeconfig_env() {
  if [ -n "${KCX_RO-}" ] || [ -n "${KCX_RUN-}" ]; then return 0; fi
  local LC_COLLATE=C f list="$HOME/.kube/config"
  if [ -n "${KCX_SHELL-}" ]; then
    if [ -n "${KUBECONFIG-}" ] && [ "$KUBECONFIG" != "${KCX_SHELL_AUTO-}" ]; then return 0; fi
    list="$KCX_SHELL:$list"
  elif [ -n "${KUBECONFIG-}" ] && [ "$KUBECONFIG" != "${KCX_AUTO-}" ]; then
    return 0
  fi
  for f in "$HOME"/.kube/configs/*.yaml "$HOME"/.kube/temp/*.yaml; do
    [ -f "$f" ] && list="$list:$f"
  done
  if [ -n "${KCX_SHELL-}" ]; then
    export KUBECONFIG="$list" KCX_SHELL_AUTO="$list"
  else
    export KUBECONFIG="$list" KCX_AUTO="$list"
  fi
}
# Inside a kcx shell, a read-only shell or a run, a startup file read after this
# snippet (another tool's) may have set KUBECONFIG; the first prompt puts the
# shell's own back, before anything typed runs.
_kcx_restore_once() {
  if [ -n "${_kcx_restored-}" ]; then return 0; fi
  _kcx_restored=1
  local LC_COLLATE=C f list="$HOME/.kube/config"
  for f in "$HOME"/.kube/configs/*.yaml "$HOME"/.kube/temp/*.yaml; do
    [ -f "$f" ] && list="$list:$f"
  done
  if [ -n "${KCX_RO_CONFIG-}" ]; then
    export KUBECONFIG="$KCX_RO_CONFIG"
  elif [ -n "${KCX_SHELL-}" ]; then
    export KUBECONFIG="$KCX_SHELL:$list" KCX_SHELL_AUTO="$KCX_SHELL:$list"
  elif [ -n "${KCX_RUN_CONFIG-}" ]; then
    export KUBECONFIG="$KCX_RUN_CONFIG:$list"
  fi
}
_kcx_kubeconfig_env
case $- in
  *i*)
    case ";${PROMPT_COMMAND-};" in
      *";_kcx_kubeconfig_env;"*) ;;
      *) PROMPT_COMMAND="_kcx_kubeconfig_env${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
    esac
    if [ -n "${KCX_RO_CONFIG-}${KCX_SHELL-}${KCX_RUN_CONFIG-}" ]; then
      case ";${PROMPT_COMMAND-};" in
        *";_kcx_restore_once;"*) ;;
        *) PROMPT_COMMAND="_kcx_restore_once${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
      esac
    fi ;;
esac
`

// pwshInit runs in PowerShell 7 and Windows PowerShell 5.1, on any platform. It
// follows Init's line, which sets $global:_kcx_kube.
const pwshInit = `# kcx: KUBECONFIG = the state file, then one file per cluster, kept current in every
# shell. A KUBECONFIG set on purpose is left alone: only a value this snippet set
# itself (KCX_AUTO) is replaced, and never inside a kcx read-only shell or a kcx run.
# Inside a kcx shell (KCX_SHELL) the shell's private file comes first, and the
# value is tracked by KCX_SHELL_AUTO instead. The list is kcx's own: *.yaml files,
# no dotfiles, each directory sorted bytewise.
function global:_kcx_files {
  $files = @(Join-Path $global:_kcx_kube 'config')
  foreach ($d in 'configs', 'temp') {
    $dir = Join-Path $global:_kcx_kube $d
    if (-not (Test-Path -LiteralPath $dir -PathType Container)) { continue }
    [string[]]$names = @(Get-ChildItem -LiteralPath $dir -File -Force |
      Where-Object { $_.Name.EndsWith('.yaml', [StringComparison]::Ordinal) -and -not $_.Name.StartsWith('.') } |
      ForEach-Object { $_.Name })
    [Array]::Sort($names, [StringComparer]::Ordinal)
    foreach ($n in $names) { $files += Join-Path $dir $n }
  }
  $files -join [IO.Path]::PathSeparator
}
function global:_kcx_kubeconfig_env {
  if ($env:KCX_RO -or $env:KCX_RUN) { return }
  $list = _kcx_files
  if ($env:KCX_SHELL) {
    if ($env:KUBECONFIG -and $env:KUBECONFIG -cne $env:KCX_SHELL_AUTO) { return }
    $list = $env:KCX_SHELL + [IO.Path]::PathSeparator + $list
    $env:KUBECONFIG = $list
    $env:KCX_SHELL_AUTO = $list
    return
  }
  if ($env:KUBECONFIG -and $env:KUBECONFIG -cne $env:KCX_AUTO) { return }
  $env:KUBECONFIG = $list
  $env:KCX_AUTO = $list
}
# Inside a kcx shell, a read-only shell or a run, a profile script read after this
# snippet (another tool's) may have set KUBECONFIG; the first prompt puts the
# shell's own back, before anything typed runs.
function global:_kcx_restore_once {
  if (Test-Path variable:global:_kcx_restored) { return }
  $global:_kcx_restored = $true
  $sep = [IO.Path]::PathSeparator
  if ($env:KCX_RO_CONFIG) {
    $env:KUBECONFIG = $env:KCX_RO_CONFIG
  } elseif ($env:KCX_SHELL) {
    $env:KUBECONFIG = $env:KCX_SHELL + $sep + (_kcx_files)
    $env:KCX_SHELL_AUTO = $env:KUBECONFIG
  } elseif ($env:KCX_RUN_CONFIG) {
    $env:KUBECONFIG = $env:KCX_RUN_CONFIG + $sep + (_kcx_files)
  }
}
_kcx_kubeconfig_env
# Before each prompt: PowerShell has no hook list, so this wraps the prompt
# function in place when it runs (load it after a theme's own init, oh-my-posh's
# or starship's), and hands that prompt the last command's $? and $LASTEXITCODE.
# Nothing here reads a variable that may not be set, for Set-StrictMode's sake.
if (-not (Test-Path variable:global:_kcx_prompt)) {
  $global:_kcx_prompt = $function:prompt
  function global:prompt {
    $ok = $?
    $code = Get-Variable -Name LASTEXITCODE -Scope Global -ValueOnly -ErrorAction Ignore
    if ($env:KCX_RO_CONFIG -or $env:KCX_SHELL -or $env:KCX_RUN_CONFIG) { _kcx_restore_once }
    _kcx_kubeconfig_env
    if ($null -ne $code) { $global:LASTEXITCODE = $code }
    if ($ok) { $null = $null } else { Write-Error '' -ErrorAction Ignore }
    & $global:_kcx_prompt
  }
}
`

const pwshCompletion = `# kcx completes its contexts, clusters and commands from its own word list.
Register-ArgumentCompleter -Native -CommandName kcx, kcx.exe -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = @($commandAst.CommandElements | Select-Object -Skip 1 |
    Where-Object { $_.Extent.EndOffset -lt $cursorPosition } | ForEach-Object { $_.Extent.Text })
  kcx __complete @words 2>$null |
    Where-Object { $_.StartsWith($wordToComplete, [StringComparison]::Ordinal) } |
    ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }
}
`

const bashCompletion = `# kcx completes its contexts, clusters and commands from its own word list.
case $- in
  *i*)
    _kcx_complete() {
      local cur="${COMP_WORDS[COMP_CWORD]}" words
      COMPREPLY=()
      words="$(kcx __complete "${COMP_WORDS[@]:1:COMP_CWORD-1}" 2>/dev/null)"
      local IFS=$'\n'
      COMPREPLY=( $(compgen -W "$words" -- "$cur") )
    }
    complete -F _kcx_complete kcx ;;
esac
`
