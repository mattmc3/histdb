# `histdb init` pins HISTDB_BIN to the binary that printed this, so a later
# change to PATH cannot stop the hooks. Falls back to PATH if it goes missing.
: ${HISTDB_BIN:=histdb}
[[ $HISTDB_BIN == */* && ! -x $HISTDB_BIN ]] && HISTDB_BIN=histdb

# EPOCHREALTIME arrived in bash 5.0, along with the negative offset that
# `history -d` is called with below.
if ((BASH_VERSINFO[0] < 5)); then
  echo "histdb: bash 5.0 or newer is required, this is $BASH_VERSION" >&2
fi

# EPOCHREALTIME's separator is the locale's and its fraction is always six
# digits, so rebuild it rather than hope for a period.
_histdb_now() { local t=${EPOCHREALTIME//[!0-9]}; printf '%s.%s\n' "${t%??????}" "${t: -6}"; }

# Bash has no $TTY, and the terminal does not change under a session.
_histdb_tty=$(tty 2>/dev/null) || _histdb_tty=

# A session is one shell process under one user. HISTDB_SESSION is exported,
# so a nested shell or an exec into sudo inherits a key that is neither, and
# the uid and pid it carries are what catch that.
if [[ ${HISTDB_SESSION:-} != "${UID}.$$."* ]]; then
  export HISTDB_SESSION="${UID}.$$.$(_histdb_now).${RANDOM}"
fi

# Kept across a re-source, since the command that re-sourced this is itself
# half recorded by then.
_histdb_cmd=${_histdb_cmd-}
_histdb_start=${_histdb_start-}
_histdb_cwd=${_histdb_cwd-}
_histdb_line=${_histdb_line-}
_histdb_histnum=${_histdb_histnum-}
_histdb_at_prompt=${_histdb_at_prompt-}
_histdb_pipes=${_histdb_pipes-}
_histdb_prev=${_histdb_prev-}
_histdb_histignore=${_histdb_histignore-}
_histdb_dupctl=${_histdb_dupctl-}
_histdb_ctlset=${_histdb_ctlset-}

# If you ran it, it is recorded. Bash disagrees: HISTIGNORE and the duplicate
# settings in HISTCONTROL keep lines out of its history list, and that list is
# the only place the whole typed line can be read back from. So they are moved
# aside here and applied again in _histdb_filter, which leaves the history file
# looking the way they would have left it.
#
# Read every prompt, since a shell can change them at any point. IGNORESPACE
# stays with bash: a leading space is how you say do not record this at all.
_histdb_absorb() {
  if [[ ${HISTCONTROL-} != "$_histdb_ctlset" ]]; then
    local ctl=:${HISTCONTROL-}:
    _histdb_dupctl=
    case $ctl in
      *:ignoredups:* | *:ignoreboth:* | *:erasedups:*) _histdb_dupctl=1 ;;
    esac
    case $ctl in
      *:ignorespace:* | *:ignoreboth:*) HISTCONTROL=ignorespace ;;
      *) HISTCONTROL= ;;
    esac
    _histdb_ctlset=$HISTCONTROL
  fi
  if [[ -n ${HISTIGNORE-} ]]; then
    _histdb_histignore=$HISTIGNORE
    HISTIGNORE=
  fi
}

# _histdb_ignored matches a line the way bash matches HISTIGNORE: colon
# separated globs against the whole line, with `&` standing for the line
# before it.
_histdb_ignored() {
  [[ -z $_histdb_histignore ]] && return 1

  local cmd=$1 pat
  # Splitting on the colons must not glob the pieces against the filesystem.
  local - IFS=:
  set -f
  for pat in $_histdb_histignore; do
    [[ -z $pat ]] && continue
    if [[ $pat == '&' ]]; then
      [[ $cmd == "$_histdb_prev" ]] && return 0
    elif [[ $cmd == $pat ]]; then
      return 0
    fi
  done
  return 1
}

# _histdb_filter puts back what bash would have left out of its history list.
# The command is already recorded by the time this runs, so only the list is
# touched, and the history file follows from it.
#
# ERASEDUPS is served as IGNOREDUPS: erasing an older copy means finding it,
# and that is a scan of the whole list on every prompt.
_histdb_filter() {
  local cmd=$1

  if [[ -n $_histdb_dupctl && $cmd == "$_histdb_prev" ]] || _histdb_ignored "$cmd"; then
    # Dropping the entry frees its number for the next line, so the count the
    # DEBUG trap compares against has to come back down with it.
    if builtin history -d -1 2>/dev/null && [[ -n $_histdb_histnum ]]; then
      _histdb_histnum=$((_histdb_histnum - 1))
    fi
    return 0
  fi
  _histdb_prev=$cmd
}

# _histdb_read_line reads the line bash just stored, since $BASH_COMMAND is
# only ever one simple command and would report `true | false` as `true`. The
# history number comes back with it, because an unchanged one is how you tell
# bash declined to store the line at all.
_histdb_read_line() {
  local raw
  raw=$(HISTTIMEFORMAT= builtin history 1)
  if [[ $raw =~ ^[[:space:]]*([0-9]+) ]]; then
    _histdb_histnum=${BASH_REMATCH[1]}
    raw=${raw#"${BASH_REMATCH[0]}"}
    # `history` stars an entry edited this session where it otherwise pads with
    # a space, then separates with one more. Trim exactly that, so a space the
    # user typed survives.
    raw=${raw#[*[:space:]]}
    _histdb_line=${raw# }
  else
    # No history list, from HISTSIZE=0.
    _histdb_histnum=
    _histdb_line=$BASH_COMMAND
  fi
}

# The DEBUG trap fires for every simple command, so only the first one after a
# prompt is what was typed at it. $_ arrives as $1 because calling this has
# already overwritten it.
_histdb_debug() {
  # First, before a `local` of its own resets it: the firing ahead of the
  # prompt hooks is the one still holding the command's own statuses.
  _histdb_pipes="${PIPESTATUS[*]}"
  local lastarg=$1

  # A completion function or a `bind -x` widget runs commands of its own, and
  # neither is a line anybody typed.
  if [[ -n $_histdb_at_prompt && -z ${COMP_POINT:-} && -z ${READLINE_POINT:-} ]]; then
    _histdb_at_prompt=

    if [[ ! -o history ]]; then
      # `set +o history` freezes the list, so the line has to come from the one
      # simple command bash names, and a pipeline arrives as its first part.
      _histdb_preexec "$BASH_COMMAND"
    else
      local was=$_histdb_histnum
      _histdb_read_line
      # With HISTIGNORE and the duplicate settings moved aside, the one thing
      # left that stops bash storing a line is IGNORESPACE, and an unchanged
      # history number is how that shows up.
      if [[ -z $_histdb_histnum || $_histdb_histnum != "$was" ]]; then
        _histdb_preexec "$_histdb_line"
      fi
    fi
  fi

  _histdb_restore "$lastarg"
}

# Exists to hand $_ back as its own last argument, which is what a following
# `cd $_` reads. The trap that ran took the real one with it.
_histdb_restore() { return 0; }

_histdb_preexec() {
  local cmd=$1
  [[ -z ${cmd//[[:space:]]/} ]] && return 0

  _histdb_cmd=$cmd
  _histdb_start=$(_histdb_now)
  # Where the command was launched, not where it left the shell sitting.
  _histdb_cwd=$PWD

  # Recorded before the command runs, so anything that outlives the shell is
  # already on disk. The precmd write fills in how it went. Backgrounded inside
  # a subshell, so the prompt neither waits on it nor reports it as a job.
  ( command "$HISTDB_BIN" record \
      --shell bash \
      --session "$HISTDB_SESSION" \
      --tty "$_histdb_tty" \
      --cwd "$PWD" \
      --cmd "$cmd" \
      --start "$_histdb_start" >/dev/null 2>&1 & )
}

_histdb_precmd() {
  # One command, since anything ahead of it would leave $PIPESTATUS its own.
  local ret=$? pipes="${PIPESTATUS[*]}"
  [[ ${_histdb_pipes##* } == "$ret" ]] && pipes=$_histdb_pipes
  # bash-preexec hands a precmd hook the exit status but not $PIPESTATUS, and
  # owns the trap that would have kept a copy, so a status that disagrees with
  # the command's is some earlier command's.
  [[ ${pipes##* } == "$ret" ]] || pipes=$ret

  if [[ -n $_histdb_cmd ]]; then
    local end
    end=$(_histdb_now)
    # Same session and start time as the preexec write, which is what pairs
    # the two.
    ( command "$HISTDB_BIN" record \
        --shell bash \
        --session "$HISTDB_SESSION" \
        --tty "$_histdb_tty" \
        --cwd "$_histdb_cwd" \
        --cmd "$_histdb_cmd" \
        --ret "$ret" \
        --pipestatus "${pipes// /,}" \
        --start "$_histdb_start" \
        --end "$end" >/dev/null 2>&1 & )
    _histdb_filter "$_histdb_cmd"
    _histdb_cmd=
  fi
  return $ret
}

# Runs last in PROMPT_COMMAND, so nothing the prompt itself does is taken for a
# typed line.
_histdb_ready() {
  local ret=$?
  _histdb_absorb
  _histdb_at_prompt=1
  return $ret
}

histdb() {
  command "$HISTDB_BIN" "$@"
}

# Nothing to hook in a script, which has no prompt to hang any of this off.
# Re-sourcing must not hook twice either: the definitions above already stand
# being repeated, and the hook lists are what need looking at.
if ((BASH_VERSINFO[0] >= 5)) && [[ $- == *i* &&
      "${PROMPT_COMMAND[*]:-} ${precmd_functions[*]:-}" != *_histdb_precmd* ]]; then
  _histdb_absorb
  if [[ -n ${bash_preexec_imported:-${__bp_imported:-}} ]]; then
    # bash-preexec owns the DEBUG trap, and taking it away would break whatever
    # else hangs off it. Its preexec hands over the whole line already.
    preexec_functions+=(_histdb_preexec)
    precmd_functions=(_histdb_precmd ${precmd_functions[@]+"${precmd_functions[@]}"} _histdb_ready)
  else
    # A DEBUG trap already here belongs to somebody, and replacing it silently
    # would take their hook away.
    _histdb_trap=$(trap -p DEBUG)
    if [[ -n $_histdb_trap ]]; then
      echo "histdb: a DEBUG trap is already installed, leaving it alone" >&2
      echo "histdb: load histdb first, or load bash-preexec to share the trap" >&2
    else
      # Start from the line that loaded this, so the first command after it is
      # compared against something rather than taken for a fresh one.
      _histdb_read_line
      _histdb_prev=$_histdb_line
      # $_ is expanded here, before the call that would overwrite it.
      trap '_histdb_debug "$_"' DEBUG
      # Two entries: the first still sees the command's exit status, the last
      # runs after anything else the prompt does.
      if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == 'declare -a'* ]]; then
        PROMPT_COMMAND=(_histdb_precmd ${PROMPT_COMMAND[@]+"${PROMPT_COMMAND[@]}"} _histdb_ready)
      else
        PROMPT_COMMAND="_histdb_precmd${PROMPT_COMMAND:+$'\n'${PROMPT_COMMAND}}"$'\n'"_histdb_ready"
      fi
    fi
    unset _histdb_trap
  fi
fi
