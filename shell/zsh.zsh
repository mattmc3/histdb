zmodload zsh/datetime 2>/dev/null

typeset -gA _histdb_state
if [[ -n ${_histdb_state[loaded]:-} ]]; then
  return 0
fi
_histdb_state[loaded]=1

# A session is one shell process under one user. HISTDB_SESSION is exported,
# so a nested shell or an exec into sudo inherits a key that is neither, and
# the uid and pid it carries are what catch that.
if [[ ${HISTDB_SESSION:-} != "${UID}.$$."* ]]; then
  export HISTDB_SESSION="${UID}.$$.${EPOCHREALTIME}.${RANDOM}"
fi

# Reasons zsh itself would keep a command out of the history list. Callers read
# $options before `emulate -L zsh`, which resets options to zsh defaults.
_histdb_skip() {
  local cmd=$1

  [[ -z ${cmd//[[:space:]]/} ]] && return 0
  [[ $2 == on && $cmd == [[:space:]]* ]] && return 0
  if [[ $3 == on ]]; then
    # HIST_NO_FUNCTIONS: `function f { }` and `f() { }`
    [[ $cmd == function[[:space:]]##* ]] && return 0
    [[ $cmd == [[:alnum:]_]##[[:space:]]#\(\)[[:space:]]#\{* ]] && return 0
  fi
  # HIST_NO_STORE drops the commands used to read history back.
  [[ $4 == on && ${cmd%%[[:space:]]*} == (history|fc) ]] && return 0
  return 1
}

_histdb_preexec() {
  local ignore_space=$options[histignorespace]
  local reduce_blanks=$options[histreduceblanks]
  local ignore_dups=$options[histignoredups]
  local no_functions=$options[histnofunctions]
  local no_store=$options[histnostore]
  emulate -L zsh
  setopt local_options extended_glob

  local cmd=$1
  _histdb_skip "$cmd" "$ignore_space" "$no_functions" "$no_store" && return 0

  if [[ $reduce_blanks == on ]]; then
    cmd="${${${cmd//[[:blank:]][[:blank:]]##/ }##[[:blank:]]##}%%[[:blank:]]##}"
  fi

  if [[ $ignore_dups == on && $cmd == ${_histdb_state[last_cmd]:-} ]]; then
    return 0
  fi

  _histdb_state[cmd]=$cmd
  _histdb_state[start_ts]=$EPOCHREALTIME
  # Where the command was launched, not where it left the shell sitting.
  _histdb_state[cwd]=$PWD
  _histdb_state[last_cmd]=$cmd

  # Recorded before the command runs, so anything that outlives the shell is
  # already on disk. The precmd write fills in how it went.
  command histdb record \
    --shell zsh \
    --session "$HISTDB_SESSION" \
    --tty "${TTY:-}" \
    --cwd "$PWD" \
    --cmd "$cmd" \
    --start "${_histdb_state[start_ts]}" >/dev/null 2>&1 &|
}

_histdb_precmd() {
  local -a saved_pipestatus=("${pipestatus[@]}")
  emulate -L zsh
  setopt local_options

  [[ -z ${_histdb_state[cmd]:-} ]] && return 0

  local end_ts=$EPOCHREALTIME
  local cmd=${_histdb_state[cmd]}
  local start_ts=${_histdb_state[start_ts]:-$end_ts}
  local ret=$saved_pipestatus[-1]

  # Same session and start time as the preexec write, which is what pairs the
  # two. Backgrounded and disowned so it stays off the prompt path.
  command histdb record \
    --shell zsh \
    --session "$HISTDB_SESSION" \
    --tty "${TTY:-}" \
    --cwd "${_histdb_state[cwd]:-$PWD}" \
    --cmd "$cmd" \
    --ret "$ret" \
    --pipestatus "${(j:,:)saved_pipestatus}" \
    --start "$start_ts" \
    --end "$end_ts" >/dev/null 2>&1 &|

  unset '_histdb_state[cmd]' '_histdb_state[start_ts]' '_histdb_state[cwd]'
}

# Searching honors the options that decide what zsh would show you. They are
# read per call, since a shell can setopt them at any point.
histdb() {
  local share=$options[sharehistory]
  local find_no_dups=$options[histfindnodups]
  emulate -L zsh
  setopt local_options

  local -a opts
  local sub

  case ${1:-} in
    init|record) command histdb "$@"; return $? ;;
    search) sub=$1; shift ;;
  esac

  # NO_SHARE_HISTORY means other shells' commands are not yours to see.
  [[ $share == off ]] && opts+=(--session)
  [[ $find_no_dups == on ]] && opts+=(--no-dups)

  # Options go after the subcommand: the binary reads argument one to choose
  # the command, so a flag in front of it would look like a search pattern.
  command histdb $sub $opts "$@"
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec _histdb_preexec
add-zsh-hook precmd _histdb_precmd

# Runs first so $pipestatus is still the command's, not an earlier hook's.
precmd_functions=(
  _histdb_precmd
  ${precmd_functions:#_histdb_precmd}
)
