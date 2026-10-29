# Generated from docs/openapi.yaml by scripts/generate-api-artifacts.py.
_stepanelctl_complete() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"; prev="${COMP_WORDS[COMP_CWORD-1]}"
  if (( COMP_CWORD == 1 )); then
    COMPREPLY=( $(compgen -W "backup capabilities completion diagnostics doctor jobs migration provision restore site sites status" -- "$cur") )
    return
  fi
  case "${COMP_WORDS[1]}" in
    backup) COMPREPLY=( $(compgen -W "create list" -- "$cur") ) ;;
    diagnostics) COMPREPLY=( $(compgen -W "collect" -- "$cur") ) ;;
    jobs) COMPREPLY=( $(compgen -W "list watch" -- "$cur") ) ;;
    migration) COMPREPLY=( $(compgen -W "import inspect" -- "$cur") ) ;;
    restore) COMPREPLY=( $(compgen -W "database files staging" -- "$cur") ) ;;
    site) COMPREPLY=( $(compgen -W "create inspect" -- "$cur") ) ;;
    sites) COMPREPLY=( $(compgen -W "list" -- "$cur") ) ;;
  esac
}
complete -F _stepanelctl_complete stepanelctl
