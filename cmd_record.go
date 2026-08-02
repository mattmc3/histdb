package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattmc3/getopt"

	"github.com/mattmc3/histdb/internal/history"
)

// runRecord writes one command. The shell calls it twice: once before the
// command runs, and once after with its outcome.
func runRecord(ctx context.Context, args []string, stderr io.Writer) error {
	fs := getopt.NewFlagSet("histdb record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	cmd := fs.Define("cmd", "", "command line that ran")
	shell := fs.Define("shell", "", "shell that ran the command")
	session := fs.Define("session", "", "shell session key")
	tty := fs.Define("tty", "", "terminal the session is attached to")
	cwd := fs.Define("cwd", "", "working directory")
	vcsRoot := fs.Define("vcs-root", "", "repository root of the working directory")
	// Shells report 0-255, so a negative default means "still running".
	ret := fs.Define("ret", -1, "exit status")
	pipeStatus := fs.Define("pipestatus", "", "comma separated pipeline statuses")
	start := fs.Define("start", 0.0, "start time in unix seconds")
	end := fs.Define("end", 0.0, "end time in unix seconds")
	meta := fs.Define("meta", "", "JSON object of extra data to attach")
	host := fs.Define("host", "", "hostname")
	who := fs.Define("user", "", "username")

	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return err
	}
	if *cmd == "" {
		return errors.New("record: --cmd is required")
	}

	// Only the start of a command knows where it ran. Guessing at the end
	// would overwrite that with wherever the shell happens to be now.
	if *cwd == "" && *end == 0 {
		if wd, err := os.Getwd(); err == nil {
			*cwd = wd
		}
	}
	if *vcsRoot == "" && *cwd != "" {
		*vcsRoot = vcsRootOf(*cwd)
	}
	if *host == "" {
		*host = shortHostname()
	}
	if *who == "" {
		*who = currentUser()
	}
	if *session == "" {
		*session = os.Getenv("HISTDB_SESSION")
	}
	if *session == "" {
		return errors.New("record: --session or HISTDB_SESSION is required")
	}
	if *tty == "" {
		*tty = os.Getenv("TTY")
	}
	if *start == 0 {
		return errors.New("record: --start is required")
	}
	// SQLite enforces this too, but its error names a constraint, not a flag.
	if *meta != "" && !json.Valid([]byte(*meta)) {
		return errors.New("record: --meta is not valid JSON")
	}
	startAt := epochToTime(*start)

	// No end time is the start-of-command write; the command is still running.
	var endAt time.Time
	if *end != 0 {
		if *ret < 0 {
			return errors.New("record: --ret is required with --end")
		}
		endAt = epochToTime(*end)
		if *pipeStatus == "" {
			*pipeStatus = fmt.Sprint(*ret)
		}
	}

	store, err := history.Open(ctx, dbPath())
	if err != nil {
		return err
	}
	defer store.Close()

	// The session row is written by whichever command in that shell records
	// first, so its start is the earliest command's start.
	sess := history.Session{
		Key:     *session,
		Shell:   *shell,
		Host:    *host,
		User:    *who,
		TTY:     *tty,
		StartAt: startAt,
	}
	if err := store.Sessions().Ensure(ctx, &sess); err != nil {
		return err
	}

	entry := &history.Entry{
		Session:    sess,
		Cwd:        *cwd,
		VCSRoot:    *vcsRoot,
		Cmd:        *cmd,
		Ret:        *ret,
		PipeStatus: *pipeStatus,
		Meta:       *meta,
		StartAt:    startAt,
		EndAt:      endAt,
	}
	if entry.Finished() {
		return store.Entries().Finish(ctx, entry)
	}
	return store.Entries().Start(ctx, entry)
}

// epochToTime converts the seconds-with-fraction that shells report, such as
// zsh's EPOCHREALTIME, into UTC.
func epochToTime(epoch float64) time.Time {
	sec, frac := math.Modf(epoch)
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

// vcsRootOf walks up from dir looking for a checkout root, returning "" when
// there is none.
func vcsRootOf(dir string) string {
	markers := []string{".git", ".hg", ".svn"}

	for dir != "" {
		for _, marker := range markers {
			if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func shortHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	host, _, _ := strings.Cut(name, ".")
	return host
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
