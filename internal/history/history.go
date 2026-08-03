// Package history records and queries shell command history.
package history

import (
	"context"
	"errors"
	"time"
)

// Session is one shell instance. Everything that stays put for the life of
// that shell lives here rather than on every command it runs.
type Session struct {
	ID      int64
	Key     string
	Shell   string
	Host    string
	User    string
	TTY     string
	StartAt time.Time
}

// Entry is one recorded command. Times are UTC. A command is recorded when it
// starts and again when it finishes, so an entry with no EndAt is either still
// running or belongs to a shell that died mid-command.
type Entry struct {
	ID         int64
	Session    Session
	Cwd        string
	VCSRoot    string
	Cmd        string
	Ret        int
	PipeStatus string
	StartAt    time.Time
	EndAt      time.Time

	// Meta is a JSON object for anything histdb itself does not model, such
	// as a tmux pane or a ticket id. Empty means no metadata was attached.
	Meta string
}

// Finished reports whether the command ran to completion. Ret and Duration
// mean nothing until it does.
func (e Entry) Finished() bool {
	return !e.EndAt.IsZero()
}

// Duration is how long the command ran.
func (e Entry) Duration() time.Duration {
	if !e.Finished() || e.EndAt.Before(e.StartAt) {
		return 0
	}
	return e.EndAt.Sub(e.StartAt)
}

// validForWrite reports whether the entry carries what identifies a command:
// the session it ran in and when it started.
func (e Entry) validForWrite() error {
	if e.Session.ID == 0 {
		return errors.New("session id is required")
	}
	if e.StartAt.IsZero() {
		return errors.New("start time is required")
	}
	return nil
}

// Status narrows a search by exit status.
type Status int

const (
	AnyStatus Status = iota
	Succeeded
	Failed
)

// Frequent is a command and how often it has been run.
type Frequent struct {
	Cmd    string
	Count  int
	LastAt time.Time
}

// Filter is the flexible query used by Search. Simple lookups do not need it.
type Filter struct {
	// Like is a SQL LIKE pattern matched against the command line, used as
	// written, so % and _ are wildcards. It is bound as a parameter, never
	// pasted into the query.
	Like string

	// Cwd alone matches one directory. Paired with VCSRoot it widens to the
	// whole checkout: this directory or anything sharing that root.
	Cwd     string
	VCSRoot string

	SessionKey string
	Status     Status
	Oldest     bool
	Limit      int

	// Unique keeps only the newest run of each distinct command, which is
	// what zsh's HIST_FIND_NO_DUPS shows.
	Unique bool

	// PreferCwd sorts commands run in that directory ahead of the rest,
	// leaving frequency to break the tie within each group. Ranking only, it
	// does not exclude anything.
	PreferCwd string

	// CurrentSessionKey hides the command the caller is running right now,
	// which is otherwise the newest row in its own output. Unfinished
	// commands from other sessions still show.
	CurrentSessionKey string
}

// Repository stores and retrieves history entries. A command is one row,
// inserted by Start and updated by Finish; the session and start time identify
// it in both calls.
type Repository interface {
	Start(ctx context.Context, e *Entry) error
	Finish(ctx context.Context, e *Entry) error
	Search(ctx context.Context, f Filter) ([]Entry, error)

	// Import adds entries a shell recorded elsewhere, and reports how many
	// rows it wrote. The rest were already stored.
	Import(ctx context.Context, entries []Entry) (int, error)

	// CountForSession is how many commands a session has stored.
	CountForSession(ctx context.Context, sessionID int64) (int, error)

	// MostFrequent ranks distinct commands by how often they ran.
	MostFrequent(ctx context.Context, f Filter) ([]Frequent, error)
}

// SessionRepository resolves the shell session a command belongs to.
type SessionRepository interface {
	Ensure(ctx context.Context, s *Session) error
}
