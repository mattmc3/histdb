package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultLimit caps a search that does not ask for a row count. NoLimit is a
// Filter.Limit asking for every match.
const (
	DefaultLimit = 20
	NoLimit      = -1
)

// migrations run in order; the index of each is the schema version it
// upgrades from.
var migrations = [][]string{
	{
		// DATETIME takes NUMERIC affinity, so the times below are stored as
		// unix epoch seconds: datetime(start_at, 'unixepoch') to read one.
		`CREATE TABLE IF NOT EXISTS sessions (
			id          INTEGER PRIMARY KEY,
			session_key TEXT NOT NULL UNIQUE,
			shell       TEXT,
			host        TEXT,
			user        TEXT,
			tty         TEXT,
			start_at    DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS history (
			id         INTEGER PRIMARY KEY,
			sid        INTEGER NOT NULL REFERENCES sessions(id),
			cwd        TEXT,
			vcs_root   TEXT,
			cmd        TEXT NOT NULL,
			ret        INTEGER,
			pipestatus TEXT,
			start_at   DATETIME,
			end_at     DATETIME,
			-- Junk drawer for whatever a shell or a user wants to attach.
			-- Queryable with json_extract(meta, '$.key').
			meta       TEXT CHECK (meta IS NULL OR json_valid(meta))
		)`,
	},
	{
		// A command is identified by the session it ran in and when it
		// started, which is what lets the start and end writes find each
		// other without passing an id back through the shell.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_history_sid_start_at ON history(sid, start_at)`,
		`CREATE INDEX IF NOT EXISTS idx_history_start_at ON history(start_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_history_cmd ON history(cmd)`,
		// Composite so --here filters and orders off one index.
		`CREATE INDEX IF NOT EXISTS idx_history_cwd_start_at ON history(cwd, start_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_history_sid ON history(sid, start_at DESC)`,
	},
}

// Store owns the database handle and hands out the repositories that read and
// write it.
type Store struct {
	db       *sql.DB
	entries  Repository
	sessions SessionRepository
}

// Open opens the database at path, creating and migrating it as needed.
// The file and its directory are kept private to the owner.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// The busy timeout covers the overlap when several shells write at once.
	dsn := "file:" + url.PathEscape(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure %s: %w", path, err)
	}
	enableWAL(ctx, db)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{
		db:       db,
		entries:  &sqliteRepository{db: db},
		sessions: &sqliteSessionRepository{db: db},
	}, nil
}

func (s *Store) Entries() Repository         { return s.entries }
func (s *Store) Sessions() SessionRepository { return s.sessions }
func (s *Store) Close() error                { return s.db.Close() }

// enableWAL keeps a backgrounded insert from blocking the next prompt. The
// mode is stored in the file, so only the first open has to set it, and a
// concurrent setter makes this one fail with SQLITE_BUSY. Best effort:
// journal mode is a throughput choice, not a correctness one.
func enableWAL(ctx context.Context, db *sql.DB) {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return
	}
	if strings.EqualFold(mode, "wal") {
		return
	}
	db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
}

// migrate brings the schema up to date. Shells write concurrently, so the
// version is read behind the write lock that BEGIN IMMEDIATE takes: a second
// process waits out the busy timeout and then sees the finished version
// rather than replaying migrations on top of them.
func migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	if err := applyMigrations(ctx, conn); err != nil {
		conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func applyMigrations(ctx context.Context, conn *sql.Conn) error {
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for v := version; v < len(migrations); v++ {
		for _, stmt := range migrations[v] {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d: %w", v, err)
			}
		}
		// PRAGMA takes no bind parameters.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return fmt.Errorf("migration %d: %w", v, err)
		}
	}
	return nil
}

type sqliteSessionRepository struct {
	db *sql.DB
}

// Ensure resolves the session by key, inserting it the first time that shell
// records anything, and fills in s.ID either way.
func (r *sqliteSessionRepository) Ensure(ctx context.Context, s *Session) error {
	if s.Key == "" {
		return errors.New("ensure session: key is required")
	}

	// The no-op update is what makes RETURNING yield the existing row.
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO sessions (session_key, shell, host, user, tty, start_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_key) DO UPDATE SET session_key = excluded.session_key
		 RETURNING id`,
		s.Key, nullText(s.Shell), nullText(s.Host), nullText(s.User),
		nullText(s.TTY), nullTime(s.StartAt),
	).Scan(&s.ID)
	if err != nil {
		return fmt.Errorf("ensure session %q: %w", s.Key, err)
	}
	return nil
}

type sqliteRepository struct {
	db *sql.DB
}

// Start inserts the command as it begins running, with no outcome yet.
func (r *sqliteRepository) Start(ctx context.Context, e *Entry) error {
	if err := e.validForWrite(); err != nil {
		return fmt.Errorf("start entry: %w", err)
	}
	if err := r.insert(ctx, e); err != nil {
		return fmt.Errorf("start entry: %w", err)
	}
	return nil
}

// Finish records how the command ended. The row is normally already there, but
// the two writes are separate processes and a fast command lets the second one
// win, so a miss inserts instead of dropping the outcome.
func (r *sqliteRepository) Finish(ctx context.Context, e *Entry) error {
	if err := e.validForWrite(); err != nil {
		return fmt.Errorf("finish entry: %w", err)
	}
	if e.EndAt.IsZero() {
		return errors.New("finish entry: end time is required")
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE history SET
		     end_at     = ?,
		     ret        = ?,
		     pipestatus = COALESCE(?, pipestatus),
		     cwd        = COALESCE(cwd, ?),
		     vcs_root   = COALESCE(vcs_root, ?),
		     meta       = COALESCE(?, meta)
		 WHERE sid = ? AND start_at = ?`,
		nullTime(e.EndAt), e.Ret, nullText(e.PipeStatus), nullText(e.Cwd),
		nullText(e.VCSRoot), nullText(e.Meta), e.Session.ID, nullTime(e.StartAt),
	)
	if err != nil {
		return fmt.Errorf("finish entry: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}

	// The start write has not landed yet. Insert, and let it collapse into
	// this row when it arrives.
	if err := r.insert(ctx, e); err != nil {
		return fmt.Errorf("finish entry: %w", err)
	}
	return nil
}

// slot is what a session already holds: start times in use, and commands by
// the second they ran in.
type slot struct {
	taken map[float64]bool
	cmds  map[secondCmd]bool
}

type secondCmd struct {
	second int64
	cmd    string
}

// CountForSession is how many commands the session has stored.
func (r *sqliteRepository) CountForSession(ctx context.Context, sessionID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM history WHERE sid = ?", sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count session %d: %w", sessionID, err)
	}
	return n, nil
}

// Import writes the entries in one transaction. The same command in the same
// second is one already stored; a different one is nudged a millisecond on,
// since (sid, start_at) is unique and dropping it would lose real history.
func (r *sqliteRepository) Import(ctx context.Context, entries []Entry) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("import: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO history (sid, cwd, vcs_root, cmd, start_at, meta)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(sid, start_at) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("import: %w", err)
	}
	defer stmt.Close()

	slots := map[int64]*slot{}
	written := 0
	for _, e := range entries {
		if err := e.validForWrite(); err != nil {
			return 0, fmt.Errorf("import: %w", err)
		}
		s, err := sessionSlots(ctx, tx, slots, e.Session.ID)
		if err != nil {
			return 0, err
		}

		start := epochOf(e.StartAt)
		second := int64(math.Floor(start))
		if s.cmds[secondCmd{second, e.Cmd}] {
			continue
		}
		start, ok := s.free(start, second)
		if !ok {
			continue
		}

		if _, err := stmt.ExecContext(ctx, e.Session.ID, nullText(e.Cwd),
			nullText(e.VCSRoot), e.Cmd, start, nullText(e.Meta)); err != nil {
			return 0, fmt.Errorf("import: %w", err)
		}
		s.taken[start] = true
		s.cmds[secondCmd{second, e.Cmd}] = true
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("import: %w", err)
	}
	return written, nil
}

// free finds an unused start time inside the same second, giving up rather
// than spilling into the next one.
func (s *slot) free(start float64, second int64) (float64, bool) {
	for step := 0; step < 1000; step++ {
		if !s.taken[start] {
			return start, true
		}
		start = float64(second) + float64(step+1)/1000
	}
	return 0, false
}

// sessionSlots reads what the session already holds, once per session.
func sessionSlots(ctx context.Context, tx *sql.Tx, cache map[int64]*slot, sid int64) (*slot, error) {
	if s, ok := cache[sid]; ok {
		return s, nil
	}

	rows, err := tx.QueryContext(ctx, "SELECT start_at, cmd FROM history WHERE sid = ?", sid)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	defer rows.Close()

	s := &slot{taken: map[float64]bool{}, cmds: map[secondCmd]bool{}}
	for rows.Next() {
		var start sql.Null[float64]
		var cmd string
		if err := rows.Scan(&start, &cmd); err != nil {
			return nil, fmt.Errorf("import: %w", err)
		}
		if start.Valid {
			s.taken[start.V] = true
			s.cmds[secondCmd{int64(math.Floor(start.V)), cmd}] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	cache[sid] = s
	return s, nil
}

// epochOf is what nullTime stores, as a number to compare against.
func epochOf(t time.Time) float64 {
	return float64(t.UTC().UnixNano()) / 1e9
}

// insert writes the row, merging with whatever the other write already stored.
// Values a caller does not know are NULL, and COALESCE keeps a NULL from
// blanking a value the other write supplied.
func (r *sqliteRepository) insert(ctx context.Context, e *Entry) error {
	// Exit status is meaningless until the command finishes, so it stays NULL
	// alongside end_at.
	var ret any
	if e.Finished() {
		ret = e.Ret
	}

	return r.db.QueryRowContext(ctx,
		`INSERT INTO history (sid, cwd, vcs_root, cmd, ret, pipestatus, start_at, end_at, meta)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(sid, start_at) DO UPDATE SET
		     cwd        = COALESCE(excluded.cwd, history.cwd),
		     vcs_root   = COALESCE(excluded.vcs_root, history.vcs_root),
		     cmd        = excluded.cmd,
		     ret        = COALESCE(excluded.ret, history.ret),
		     pipestatus = COALESCE(excluded.pipestatus, history.pipestatus),
		     end_at     = COALESCE(excluded.end_at, history.end_at),
		     meta       = COALESCE(excluded.meta, history.meta)
		 RETURNING id`,
		e.Session.ID, nullText(e.Cwd), nullText(e.VCSRoot), e.Cmd, ret,
		nullText(e.PipeStatus), nullTime(e.StartAt), nullTime(e.EndAt),
		nullText(e.Meta),
	).Scan(&e.ID)
}

// where turns a filter into a WHERE clause and its arguments, shared so Search
// and MostFrequent cannot drift apart on what a filter means.
func (f Filter) where() (string, []any) {
	conditions := []string{"1=1"}
	args := []any{}

	// Taken as written, wildcards and all. Still a bound parameter, so the
	// worst a caller can do is match more rows than they meant to. A literal
	// wildcard is escaped with a backslash by the caller.
	if f.Like != "" {
		conditions = append(conditions, `h.cmd LIKE ? ESCAPE '\'`)
		args = append(args, f.Like)
	}
	switch {
	case f.Cwd != "" && f.VCSRoot != "":
		conditions = append(conditions, "(h.cwd = ? OR h.vcs_root = ?)")
		args = append(args, f.Cwd, f.VCSRoot)
	case f.Cwd != "":
		conditions = append(conditions, "h.cwd = ?")
		args = append(args, f.Cwd)
	case f.VCSRoot != "":
		conditions = append(conditions, "h.vcs_root = ?")
		args = append(args, f.VCSRoot)
	}
	if f.SessionKey != "" {
		conditions = append(conditions, "s.session_key = ?")
		args = append(args, f.SessionKey)
	}
	if f.CurrentSessionKey != "" {
		conditions = append(conditions, "NOT (h.end_at IS NULL AND s.session_key = ?)")
		args = append(args, f.CurrentSessionKey)
	}
	switch f.Status {
	case Succeeded:
		conditions = append(conditions, "h.ret = 0")
	case Failed:
		conditions = append(conditions, "h.ret != 0")
	}
	return strings.Join(conditions, " AND "), args
}

func (f Filter) rowLimit() int {
	switch {
	case f.Limit < 0:
		// SQLite reads a negative LIMIT as no limit at all.
		return NoLimit
	case f.Limit == 0:
		return DefaultLimit
	}
	return f.Limit
}

// MostFrequent ranks distinct commands by how often they ran. Suggestion
// strategies use it with a prefix, so the ordering has to be stable:
// frequency, then most recent, then the command text.
func (r *sqliteRepository) MostFrequent(ctx context.Context, f Filter) ([]Frequent, error) {
	where, args := f.where()

	// Ranking cwd first has to be an aggregate; a bare column under GROUP BY
	// would come from an arbitrary row of the group.
	order := "runs DESC, last_at DESC, cmd ASC"
	if f.PreferCwd != "" {
		order = "MIN(CASE WHEN h.cwd = ? THEN 0 ELSE 1 END), " + order
		args = append(args, f.PreferCwd)
	}
	args = append(args, f.rowLimit())

	query := fmt.Sprintf(`
		SELECT h.cmd AS cmd, COUNT(*) AS runs, MAX(h.start_at) AS last_at
		FROM history h
		JOIN sessions s ON s.id = h.sid
		WHERE %s
		GROUP BY h.cmd
		ORDER BY %s
		LIMIT ?`, where, order)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("most frequent commands: %w", err)
	}
	defer rows.Close()

	var top []Frequent
	for rows.Next() {
		var f Frequent
		var lastAt sql.Null[float64]
		if err := rows.Scan(&f.Cmd, &f.Count, &lastAt); err != nil {
			return nil, fmt.Errorf("most frequent commands: %w", err)
		}
		f.LastAt = fromEpoch(lastAt)
		top = append(top, f)
	}
	return top, rows.Err()
}

func (r *sqliteRepository) Search(ctx context.Context, f Filter) ([]Entry, error) {
	where, args := f.where()

	// Take the slice off whichever end was asked for, then hand it back
	// oldest first like `fc -li`.
	scan := "DESC"
	if f.Oldest {
		scan = "ASC"
	}
	args = append(args, f.rowLimit())

	// Grouping on cmd leaves one row per command. SQLite answers the bare
	// columns from the row that produced the MAX, so that row is the newest
	// run of it.
	startCol, group := "h.start_at", ""
	if f.Unique {
		startCol, group = "MAX(h.start_at)", "GROUP BY h.cmd"
	}

	query := fmt.Sprintf(`
		SELECT id, cwd, vcs_root, cmd, ret, pipestatus, start_at, end_at, meta,
		       sid, session_key, shell, host, user, tty, session_start
		FROM (
			SELECT h.id, h.cwd, h.vcs_root, h.cmd, h.ret, h.pipestatus,
			       %s AS start_at, h.end_at, h.meta,
			       s.id AS sid, s.session_key, s.shell, s.host, s.user, s.tty,
			       s.start_at AS session_start
			FROM history h
			JOIN sessions s ON s.id = h.sid
			WHERE %s
			%s
			ORDER BY start_at %s
			LIMIT ?
		)
		ORDER BY start_at ASC`,
		startCol, where, group, scan)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search history: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var cwd, vcsRoot, pipeStatus, meta, shell, host, who, tty sql.Null[string]
		var ret sql.Null[int64]
		var startAt, endAt, sessionStart sql.Null[float64]

		if err := rows.Scan(&e.ID, &cwd, &vcsRoot, &e.Cmd, &ret, &pipeStatus,
			&startAt, &endAt, &meta, &e.Session.ID, &e.Session.Key, &shell,
			&host, &who, &tty, &sessionStart); err != nil {
			return nil, fmt.Errorf("search history: %w", err)
		}

		e.Cwd, e.VCSRoot, e.PipeStatus, e.Meta = cwd.V, vcsRoot.V, pipeStatus.V, meta.V
		e.Ret = int(ret.V)
		e.StartAt, e.EndAt = fromEpoch(startAt), fromEpoch(endAt)
		e.Session.Shell, e.Session.Host = shell.V, host.V
		e.Session.User, e.Session.TTY = who.V, tty.V
		e.Session.StartAt = fromEpoch(sessionStart)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// nullText stores an unknown value as NULL rather than an empty string.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullTime writes a DATETIME column: unix epoch seconds, NULL for a zero time.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return epochOf(t)
}

// fromEpoch reverses nullTime. Rounding to the microsecond drops the float
// noise, and no shell reports finer than that.
func fromEpoch(v sql.Null[float64]) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	sec, frac := math.Modf(v.V)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC().Round(time.Microsecond)
}
