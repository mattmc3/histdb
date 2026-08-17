zmodload zsh/datetime 2>/dev/null

typeset -gA _histdb_state
if [[ -n ${_histdb_state[loaded]:-} ]]; then
  return 0
fi
_histdb_state[loaded]=1

# `histdb init` pins HISTDB_BIN to the binary that printed this, so a later
# change to PATH cannot stop the hooks. Falls back to PATH if it goes missing.
: ${HISTDB_BIN:=histdb}
[[ $HISTDB_BIN == */* && ! -x $HISTDB_BIN ]] && HISTDB_BIN=histdb

# A session is one shell process under one user. HISTDB_SESSION is exported,
# so a nested shell or an exec into sudo inherits a key that is neither, and
# the uid and pid it carries are what catch that.
if [[ ${HISTDB_SESSION:-} != "${UID}.$$."* ]]; then
  export HISTDB_SESSION="${UID}.$$.${EPOCHREALTIME}.${RANDOM}"
fi

# If you ran it, it is recorded. The options that keep a command out of zsh's
# own history list are left to zsh, which applies them to its history file
# either way, and are not a reason to drop the row here.
#
# HIST_IGNORE_SPACE is the exception: a leading space is how you say do not
# record this at all. Callers read $options before `emulate -L zsh`, which
# resets options to zsh defaults.
_histdb_skip() {
  local cmd=$1

  [[ -z ${cmd//[[:space:]]/} ]] && return 0
  [[ $2 == on && $cmd == [[:space:]]* ]] && return 0
  return 1
}

_histdb_preexec() {
  local ignore_space=$options[histignorespace]
  local reduce_blanks=$options[histreduceblanks]
  emulate -L zsh
  setopt local_options extended_glob

  local cmd=$1
  _histdb_skip "$cmd" "$ignore_space" && return 0

  if [[ $reduce_blanks == on ]]; then
    cmd="${${${cmd//[[:blank:]][[:blank:]]##/ }##[[:blank:]]##}%%[[:blank:]]##}"
  fi

  _histdb_state[cmd]=$cmd
  _histdb_state[start_ts]=$EPOCHREALTIME
  # Where the command was launched, not where it left the shell sitting.
  _histdb_state[cwd]=$PWD

  # Recorded before the command runs, so anything that outlives the shell is
  # already on disk. The precmd write fills in how it went.
  command "$HISTDB_BIN" record \
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
  command "$HISTDB_BIN" record \
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
    init|record|import) command "$HISTDB_BIN" "$@"; return $? ;;
    search) sub=$1; shift ;;
  esac

  # NO_SHARE_HISTORY means other shells' commands are not available to see, until
  # you ask for them with -S.
  [[ $share == off ]] && opts+=(--session)
  [[ $find_no_dups == on ]] && opts+=(--no-dups)

  # Options go after the subcommand: the binary reads argument one to choose
  # the command, so a flag in front of it would look like a search pattern.
  command "$HISTDB_BIN" $sub $opts "$@"
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec _histdb_preexec
add-zsh-hook precmd _histdb_precmd

# Runs first so $pipestatus is still the command's, not an earlier hook's.
precmd_functions=(
  _histdb_precmd
  ${precmd_functions:#_histdb_precmd}
)
