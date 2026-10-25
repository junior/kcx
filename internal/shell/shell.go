// Package shell holds what kcx prints for a shell to evaluate: the KUBECONFIG
// rule in pure shell (no kcx run per shell start), the guard for a read-only
// shell or a run, a hook that recomputes the variable before each prompt, and
// the completion that calls kcx's hidden word list.
//
// The names KCX_AUTO, KCX_RO and KCX_RUN and the function _kcx_kubeconfig_env
// are a contract with the dotfiles that already carry the zsh version inline.
package shell

import "fmt"

// Init returns the snippet for `eval "$(kcx init SHELL)"`.
func Init(sh string) (string, error) {
	switch sh {
	case "zsh":
		return zshInit + zshCompletion, nil
	case "bash":
		return bashInit + bashCompletion, nil
	}
	return "", fmt.Errorf("kcx init zsh|bash: %q is not a shell kcx knows", sh)
}

// Completion returns only the completion part, for shells that already carry
// the KUBECONFIG rule elsewhere (the dotfiles' ~/.zshenv).
func Completion(sh string) (string, error) {
	switch sh {
	case "zsh":
		return zshCompletion, nil
	case "bash":
		return bashCompletion, nil
	}
	return "", fmt.Errorf("kcx completion zsh|bash: %q is not a shell kcx knows", sh)
}

const zshInit = `# kcx: KUBECONFIG = the state file, then one file per cluster, kept current in every
# shell. A KUBECONFIG set on purpose is left alone: only a value this function set
# itself (KCX_AUTO) is replaced, and never inside a kcx read-only shell or a kcx run.
_kcx_kubeconfig_env() {
  emulate -L zsh
  [[ -n ${KCX_RO-} || -n ${KCX_RUN-} ]] && return 0
  [[ -z ${KUBECONFIG-} || $KUBECONFIG == ${KCX_AUTO-} ]] || return 0
  local -a files
  files=( ~/.kube/config ~/.kube/configs/*.yaml(N.on) ~/.kube/temp/*.yaml(N.on) )
  export KUBECONFIG=${(j.:.)files} KCX_AUTO=${(j.:.)files}
}
_kcx_kubeconfig_env
if [[ -o interactive ]]; then
  typeset -ga precmd_functions
  (( ${precmd_functions[(Ie)_kcx_kubeconfig_env]} )) || precmd_functions+=(_kcx_kubeconfig_env)
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
_kcx_kubeconfig_env() {
  if [ -n "${KCX_RO-}" ] || [ -n "${KCX_RUN-}" ]; then return 0; fi
  if [ -n "${KUBECONFIG-}" ] && [ "$KUBECONFIG" != "${KCX_AUTO-}" ]; then return 0; fi
  local LC_COLLATE=C f list="$HOME/.kube/config"
  for f in "$HOME"/.kube/configs/*.yaml "$HOME"/.kube/temp/*.yaml; do
    [ -f "$f" ] && list="$list:$f"
  done
  export KUBECONFIG="$list" KCX_AUTO="$list"
}
_kcx_kubeconfig_env
case $- in
  *i*)
    case ";${PROMPT_COMMAND-};" in
      *";_kcx_kubeconfig_env;"*) ;;
      *) PROMPT_COMMAND="_kcx_kubeconfig_env${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
    esac ;;
esac
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
