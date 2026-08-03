#!/usr/bin/env zsh
#
# One-time import of the pre-histdb zsh_history.db into a histdb database.
# Delete this once your history is moved over.
#
#   import-legacy-zsh-history.zsh [legacy.db] [histdb.db]
#
# Re-running it adds nothing: sessions keep their legacy key and a command is
# identified by its session and start time, as it is everywhere else.

emulate -L zsh
setopt err_exit pipe_fail no_unset

local data_home=${XDG_DATA_HOME:-$HOME/.local/share}
local legacy=${1:-$data_home/zsh/zsh_history.db}
local target=${2:-${HISTDB_FILE:-$data_home/histdb/histdb.db}}

if (( ! $+commands[sqlite3] )); then
  print -u2 "need sqlite3"
  exit 1
fi
if [[ ! -f $legacy ]]; then
  print -u2 "no legacy database at $legacy"
  exit 1
fi
# The schema and its migrations live in the binary, not here.
if [[ ! -f $target ]]; then
  print -u2 "no histdb database at $target, run histdb once to create it"
  exit 1
fi

# The legacy schema changed over the years, so take the columns from the file
# rather than assume them. A column that was never there reads as NULL.
local -a have
have=(${(f)"$(sqlite3 $legacy "SELECT name FROM pragma_table_info('zsh_history')")"})
if (( ! $#have )); then
  print -u2 "no zsh_history table in $legacy"
  exit 1
fi

col() {
  (( $have[(Ie)$1] )) && print "h.$1" || print "NULL"
}
sessioncol() {
  (( $have[(Ie)$1] )) && print "MAX($1)" || print "NULL"
}

for required in cmd sid start_ts; do
  if (( ! $have[(Ie)$required] )); then
    print -u2 "$legacy has no $required column, nothing to import from"
    exit 1
  fi
done
print "legacy columns: ${have}"

local backup=$target.$(date +%Y%m%d%H%M%S).bak
cp $target $backup

local before=$(sqlite3 $target 'SELECT COUNT(*) FROM history')

# Doubled quotes: a path is text to SQLite like any other.
local legacy_sql=${legacy//\'/\'\'}

sqlite3 $target <<SQL
ATTACH DATABASE '$legacy_sql' AS legacy;
BEGIN;

INSERT OR IGNORE INTO sessions (session_key, shell, host, user, start_at)
SELECT 'legacy:' || COALESCE(sid, 'unknown'), 'zsh',
       $(sessioncol host), $(sessioncol user), MIN(start_ts)
FROM legacy.zsh_history
GROUP BY COALESCE(sid, 'unknown');

INSERT OR IGNORE INTO history (sid, cwd, vcs_root, cmd, ret, pipestatus, start_at, end_at)
SELECT s.id, $(col cwd), $(col vcs_root), h.cmd, $(col ret), $(col pipestatus),
       h.start_ts, $(col end_ts)
FROM legacy.zsh_history h
JOIN sessions s ON s.session_key = 'legacy:' || COALESCE(h.sid, 'unknown')
WHERE h.cmd IS NOT NULL AND h.cmd <> '' AND h.start_ts IS NOT NULL;

COMMIT;
SQL

local after=$(sqlite3 $target 'SELECT COUNT(*) FROM history')
local legacy_rows=$(sqlite3 $legacy 'SELECT COUNT(*) FROM zsh_history')

print "imported $(( after - before )) of $legacy_rows rows into $target"
print "backup at $backup"
