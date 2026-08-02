package history

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

// at returns a time offset from a fixed base, so ordering is explicit.
func at(seconds int) time.Time {
	return epoch.Add(time.Duration(seconds) * time.Second)
}

func openTemp(t *testing.T) *Store {
	t.Helper()

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "histdb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func ensure(t *testing.T, store *Store, key string) Session {
	t.Helper()

	s := Session{Key: key, Shell: "zsh", Host: "box", User: "matt", TTY: "ttys001", StartAt: at(0)}
	if err := store.Sessions().Ensure(context.Background(), &s); err != nil {
		t.Fatalf("ensure session %q: %v", key, err)
	}
	return s
}

// add runs a command through both writes, the way a shell does. Tests about
// the unfinished half call Start or Finish directly.
func add(t *testing.T, store *Store, e Entry) {
	t.Helper()

	if e.Session.ID == 0 {
		e.Session = ensure(t, store, "session-1")
	}
	if e.EndAt.IsZero() {
		e.EndAt = e.StartAt
	}
	if err := store.Entries().Start(context.Background(), &e); err != nil {
		t.Fatalf("start %q: %v", e.Cmd, err)
	}
	if err := store.Entries().Finish(context.Background(), &e); err != nil {
		t.Fatalf("finish %q: %v", e.Cmd, err)
	}
}

func cmds(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Cmd
	}
	return out
}

func search(t *testing.T, store *Store, f Filter) []string {
	t.Helper()

	entries, err := store.Entries().Search(context.Background(), f)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return cmds(entries)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOpenCreatesPrivateDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "histdb.db")

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("db mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
}

func TestAddThenSearchRoundTrip(t *testing.T) {
	store := openTemp(t)
	want := Entry{
		Session: ensure(t, store, "session-1"), Cwd: "/tmp", VCSRoot: "/tmp/repo",
		Cmd: "ls -la", Ret: 2, PipeStatus: "0,2",
		StartAt: at(100), EndAt: at(100).Add(750 * time.Millisecond),
	}
	add(t, store, want)

	got, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}

	e := got[0]
	if e.ID == 0 {
		t.Error("ID = 0, want assigned")
	}
	e.ID = 0
	if !e.StartAt.Equal(want.StartAt) || !e.EndAt.Equal(want.EndAt) {
		t.Errorf("times = %v..%v, want %v..%v", e.StartAt, e.EndAt, want.StartAt, want.EndAt)
	}
	if d := e.Duration(); d != 750*time.Millisecond {
		t.Errorf("duration = %v, want 750ms", d)
	}
	if e.Cmd != want.Cmd || e.Cwd != want.Cwd || e.VCSRoot != want.VCSRoot ||
		e.Ret != want.Ret || e.PipeStatus != want.PipeStatus {
		t.Errorf("entry = %+v, want %+v", e, want)
	}
}

// Microseconds survive the round trip; zsh reports EPOCHREALTIME that fine.
func TestTimesKeepMicroseconds(t *testing.T) {
	store := openTemp(t)
	start := at(0).Add(123456 * time.Microsecond)
	add(t, store, Entry{Cmd: "ls", StartAt: start, EndAt: start.Add(time.Microsecond)})

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := entries[0].StartAt; !got.Equal(start) {
		t.Errorf("start = %v, want %v", got, start)
	}
	if got := entries[0].Duration(); got != time.Microsecond {
		t.Errorf("duration = %v, want 1us", got)
	}
}

func TestTimesAreStoredUTC(t *testing.T) {
	store := openTemp(t)
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	local := time.Date(2026, 8, 2, 6, 0, 0, 0, denver)
	add(t, store, Entry{Cmd: "ls", StartAt: local, EndAt: local})

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := entries[0].StartAt
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
	if !got.Equal(local) {
		t.Errorf("start = %v, want the same instant as %v", got, local)
	}
}

// Unknown values are absent, not empty, and the DATETIME columns hold epoch
// seconds SQLite's own date functions can read.
func TestUnknownValuesAreNullAndTimesAreEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "histdb.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	sess := Session{Key: "session-1", Shell: "zsh", StartAt: at(0)}
	if err := store.Sessions().Ensure(context.Background(), &sess); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := store.Entries().Finish(context.Background(), &Entry{
		Session: sess, Cmd: "ls", StartAt: at(0), EndAt: at(1),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	store.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var cwdType, vcsType, ttyType string
	if err := db.QueryRow(`SELECT typeof(h.cwd), typeof(h.vcs_root), typeof(s.tty)
		FROM history h JOIN sessions s ON s.id = h.sid`).Scan(&cwdType, &vcsType, &ttyType); err != nil {
		t.Fatalf("typeof: %v", err)
	}
	for name, got := range map[string]string{"cwd": cwdType, "vcs_root": vcsType, "tty": ttyType} {
		if got != "null" {
			t.Errorf("typeof(%s) = %q, want null", name, got)
		}
	}

	var stored string
	if err := db.QueryRow(`SELECT datetime(start_at, 'unixepoch') FROM history`).Scan(&stored); err != nil {
		t.Fatalf("datetime: %v", err)
	}
	if want := at(0).Format("2006-01-02 15:04:05"); stored != want {
		t.Errorf("datetime(start_at,'unixepoch') = %q, want %q", stored, want)
	}
}

func TestMetaRoundTripsAndIsQueryable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "histdb.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sess := ensure(t, store, "session-1")
	meta := `{"pane":"%3","ticket":"ABC-1"}`
	if err := store.Entries().Finish(context.Background(), &Entry{
		Session: sess, Cmd: "ls", StartAt: at(1), EndAt: at(2), Meta: meta,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := entries[0].Meta; got != meta {
		t.Errorf("meta = %q, want %q", got, meta)
	}
	store.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var pane string
	if err := db.QueryRow(`SELECT json_extract(meta, '$.pane') FROM history`).Scan(&pane); err != nil {
		t.Fatalf("json_extract: %v", err)
	}
	if pane != "%3" {
		t.Errorf("json_extract pane = %q, want %%3", pane)
	}
}

func TestMetaRejectsNonJSON(t *testing.T) {
	store := openTemp(t)
	sess := ensure(t, store, "session-1")

	err := store.Entries().Start(context.Background(), &Entry{
		Session: sess, Cmd: "ls", StartAt: at(1), Meta: "not json",
	})
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// The end write leaves metadata the start write attached alone.
func TestMetaSurvivesFinish(t *testing.T) {
	store := openTemp(t)
	sess := ensure(t, store, "session-1")
	start := at(1)

	if err := store.Entries().Start(context.Background(), &Entry{
		Session: sess, Cmd: "ls", StartAt: start, Meta: `{"pane":"%3"}`,
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.Entries().Finish(context.Background(), &Entry{
		Session: sess, Cmd: "ls", StartAt: start, EndAt: at(2),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got, want := entries[0].Meta, `{"pane":"%3"}`; got != want {
		t.Errorf("meta = %q, want %q", got, want)
	}
}

func TestEnsureSessionIsIdempotent(t *testing.T) {
	store := openTemp(t)

	first := ensure(t, store, "session-1")
	second := ensure(t, store, "session-1")
	if first.ID == 0 {
		t.Fatal("session id = 0, want assigned")
	}
	if first.ID != second.ID {
		t.Errorf("ids %d and %d differ for one key", first.ID, second.ID)
	}

	if other := ensure(t, store, "session-2"); other.ID == first.ID {
		t.Errorf("distinct keys share id %d", other.ID)
	}
}

func TestEnsureSessionRequiresKey(t *testing.T) {
	store := openTemp(t)

	s := Session{Shell: "zsh"}
	if err := store.Sessions().Ensure(context.Background(), &s); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestSearchReturnsSessionDetail(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "ls", StartAt: at(1)})

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	got := entries[0].Session
	want := Session{ID: got.ID, Key: "session-1", Shell: "zsh", Host: "box",
		User: "matt", TTY: "ttys001", StartAt: at(0)}
	if got != want {
		t.Errorf("session = %+v, want %+v", got, want)
	}
}

func TestStartAndFinishRequireSessionAndStart(t *testing.T) {
	store := openTemp(t)
	sess := ensure(t, store, "session-1")

	if err := store.Entries().Start(context.Background(), &Entry{Cmd: "ls", StartAt: at(1)}); err == nil {
		t.Fatal("start without session: want error, got nil")
	}
	if err := store.Entries().Start(context.Background(), &Entry{Session: sess, Cmd: "ls"}); err == nil {
		t.Fatal("start without start time: want error, got nil")
	}
	if err := store.Entries().Finish(context.Background(), &Entry{Session: sess, Cmd: "ls", StartAt: at(1)}); err == nil {
		t.Fatal("finish without end time: want error, got nil")
	}
}

// One row per command: inserted when it starts, updated when it finishes.
func TestRecordStartThenFinish(t *testing.T) {
	store := openTemp(t)
	sess := ensure(t, store, "session-1")
	start := at(10)

	running := &Entry{Session: sess, Cmd: "sleep 5", Cwd: "/tmp", StartAt: start}
	if err := store.Entries().Start(context.Background(), running); err != nil {
		t.Fatalf("start: %v", err)
	}

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Finished() {
		t.Error("entry reports finished before the end write")
	}

	done := &Entry{Session: sess, Cmd: "sleep 5", StartAt: start,
		EndAt: start.Add(5 * time.Second), Ret: 3, PipeStatus: "3"}
	if err := store.Entries().Finish(context.Background(), done); err != nil {
		t.Fatalf("finish: %v", err)
	}

	entries, err = store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 updated in place", len(entries))
	}
	e := entries[0]
	if !e.Finished() || e.Ret != 3 || e.Duration() != 5*time.Second {
		t.Errorf("entry = %+v, want finished ret 3 lasting 5s", e)
	}
	if e.Cwd != "/tmp" {
		t.Errorf("cwd = %q, want /tmp carried over from the start write", e.Cwd)
	}
}

// The two writes are separate processes, and for a fast command the finish
// write reaches the database first about a third of the time. Finish inserts
// so the outcome is not lost, and the late start write must not blank it.
func TestFinishBeforeStart(t *testing.T) {
	store := openTemp(t)
	sess := ensure(t, store, "session-1")
	start := at(10)

	done := &Entry{Session: sess, Cmd: "ls", StartAt: start,
		EndAt: start.Add(time.Second), Ret: 0, PipeStatus: "0"}
	if err := store.Entries().Finish(context.Background(), done); err != nil {
		t.Fatalf("finish: %v", err)
	}
	late := &Entry{Session: sess, Cmd: "ls", Cwd: "/tmp", StartAt: start}
	if err := store.Entries().Start(context.Background(), late); err != nil {
		t.Fatalf("start: %v", err)
	}

	entries, err := store.Entries().Search(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if !entries[0].Finished() {
		t.Error("late start write blanked the outcome")
	}
	if entries[0].Cwd != "/tmp" {
		t.Errorf("cwd = %q, want the late start write to fill it in", entries[0].Cwd)
	}
}

// A running command should not appear in the output of the command that is
// running it, but another shell's unfinished command still shows.
func TestSearchHidesOwnRunningCommand(t *testing.T) {
	store := openTemp(t)
	mine := ensure(t, store, "session-mine")
	theirs := ensure(t, store, "session-theirs")

	if err := store.Entries().Start(context.Background(),
		&Entry{Session: mine, Cmd: "histdb", StartAt: at(3)}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.Entries().Start(context.Background(),
		&Entry{Session: theirs, Cmd: "sleep 900", StartAt: at(2)}); err != nil {
		t.Fatalf("start: %v", err)
	}
	add(t, store, Entry{Session: mine, Cmd: "ls", StartAt: at(1)})

	got := search(t, store, Filter{CurrentSessionKey: "session-mine"})
	if want := []string{"ls", "sleep 900"}; !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSearchOrdersOldestFirst(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "first", StartAt: at(1)})
	add(t, store, Entry{Cmd: "second", StartAt: at(2)})
	add(t, store, Entry{Cmd: "third", StartAt: at(3)})

	if got, want := search(t, store, Filter{}), []string{"first", "second", "third"}; !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The newest rows are the default slice, and each is still printed oldest
// first, so a limit takes from the tail.
func TestSearchLimitTakesNewest(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "first", StartAt: at(1)})
	add(t, store, Entry{Cmd: "second", StartAt: at(2)})
	add(t, store, Entry{Cmd: "third", StartAt: at(3)})

	if got, want := search(t, store, Filter{Limit: 2}), []string{"second", "third"}; !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSearchOldestTakesHead(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "first", StartAt: at(1)})
	add(t, store, Entry{Cmd: "second", StartAt: at(2)})
	add(t, store, Entry{Cmd: "third", StartAt: at(3)})

	if got, want := search(t, store, Filter{Limit: 2, Oldest: true}), []string{"first", "second"}; !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	store := openTemp(t)
	for i := range DefaultLimit + 5 {
		add(t, store, Entry{Cmd: fmt.Sprintf("cmd-%d", i), StartAt: at(i)})
	}

	if got := search(t, store, Filter{}); len(got) != DefaultLimit {
		t.Errorf("got %d entries, want %d", len(got), DefaultLimit)
	}
}

func TestSearchFilters(t *testing.T) {
	store := openTemp(t)
	one := ensure(t, store, "session-1")
	two := ensure(t, store, "session-2")
	add(t, store, Entry{Session: one, Cmd: "git status", Cwd: "/a", Ret: 0, StartAt: at(1)})
	add(t, store, Entry{Session: two, Cmd: "git push", Cwd: "/b", Ret: 1, StartAt: at(2)})
	add(t, store, Entry{Session: two, Cmd: "ls", Cwd: "/a", Ret: 0, StartAt: at(3)})

	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"like", Filter{Like: "%git%"}, []string{"git status", "git push"}},
		{"cwd", Filter{Cwd: "/a"}, []string{"git status", "ls"}},
		{"session", Filter{SessionKey: "session-2"}, []string{"git push", "ls"}},
		{"failed", Filter{Status: Failed}, []string{"git push"}},
		{"succeeded", Filter{Status: Succeeded}, []string{"git status", "ls"}},
		{"combined", Filter{Like: "%git%", Status: Succeeded}, []string{"git status"}},
		{"no match", Filter{Like: "%nope%"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := search(t, store, tt.filter); !equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --repo widens --here to the whole checkout without dragging in unrelated
// directories.
func TestSearchByRepo(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "here", Cwd: "/repo/sub", VCSRoot: "/repo", StartAt: at(1)})
	add(t, store, Entry{Cmd: "elsewhere in repo", Cwd: "/repo/other", VCSRoot: "/repo", StartAt: at(2)})
	add(t, store, Entry{Cmd: "another repo", Cwd: "/elsewhere", VCSRoot: "/elsewhere", StartAt: at(3)})
	add(t, store, Entry{Cmd: "no repo", Cwd: "/tmp", StartAt: at(4)})
	// Run here back when the directory was not a checkout yet.
	add(t, store, Entry{Cmd: "here before git init", Cwd: "/repo/sub", StartAt: at(5)})

	t.Run("directory only", func(t *testing.T) {
		got := search(t, store, Filter{Cwd: "/repo/sub"})
		if want := []string{"here", "here before git init"}; !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	// Directory or repository, so a row missing one still matches on the other.
	t.Run("whole repo", func(t *testing.T) {
		got := search(t, store, Filter{Cwd: "/repo/sub", VCSRoot: "/repo"})
		want := []string{"here", "elsewhere in repo", "here before git init"}
		if !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("outside a checkout falls back to the directory", func(t *testing.T) {
		got := search(t, store, Filter{Cwd: "/tmp"})
		if want := []string{"no repo"}; !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// HIST_FIND_NO_DUPS: one row per command, the newest run of it.
func TestSearchUnique(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "ls", Cwd: "/a", StartAt: at(1)})
	add(t, store, Entry{Cmd: "git status", Cwd: "/a", StartAt: at(2)})
	add(t, store, Entry{Cmd: "ls", Cwd: "/b", StartAt: at(3)})

	got := search(t, store, Filter{Unique: true})
	if want := []string{"git status", "ls"}; !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	entries, err := store.Entries().Search(context.Background(), Filter{Unique: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, e := range entries {
		if e.Cmd == "ls" && e.Cwd != "/b" {
			t.Errorf("ls came from %q, want the newest run in /b", e.Cwd)
		}
	}
}

func TestSearchUniqueRespectsLimit(t *testing.T) {
	store := openTemp(t)
	for i := range 5 {
		add(t, store, Entry{Cmd: "ls", StartAt: at(i)})
		add(t, store, Entry{Cmd: fmt.Sprintf("cmd-%d", i), StartAt: at(100 + i)})
	}

	if got := search(t, store, Filter{Unique: true, Limit: 3}); len(got) != 3 {
		t.Errorf("got %d rows, want 3: %v", len(got), got)
	}
}

func mostFrequent(t *testing.T, store *Store, f Filter) []string {
	t.Helper()

	got, err := store.Entries().MostFrequent(context.Background(), f)
	if err != nil {
		t.Fatalf("most frequent: %v", err)
	}
	out := make([]string, len(got))
	for i, c := range got {
		out[i] = c.Cmd
	}
	return out
}

// Ranked by how often a command ran, and matched from the start of the command
// line so a suggestion strategy can complete what has been typed.
func TestMostFrequent(t *testing.T) {
	store := openTemp(t)
	runs := map[string]int{
		"git push":   5,
		"git pull":   3,
		"git status": 1,
		"grep foo":   9,
	}
	start := 0
	for cmd, n := range runs {
		for range n {
			start++
			add(t, store, Entry{Cmd: cmd, StartAt: at(start)})
		}
	}

	t.Run("ranked by frequency", func(t *testing.T) {
		got := mostFrequent(t, store, Filter{})
		want := []string{"grep foo", "git push", "git pull", "git status"}
		if !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("anchored pattern matches only the start", func(t *testing.T) {
		got := mostFrequent(t, store, Filter{Like: "git p%"})
		if want := []string{"git push", "git pull"}; !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("an anchored pattern is not a substring match", func(t *testing.T) {
		if got := mostFrequent(t, store, Filter{Like: "push%"}); len(got) != 0 {
			t.Errorf("got %v, want nothing: push is not at the start", got)
		}
	})

	t.Run("counts are reported", func(t *testing.T) {
		got, err := store.Entries().MostFrequent(context.Background(), Filter{Like: "grep%"})
		if err != nil {
			t.Fatalf("most frequent: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d rows, want 1", len(got))
		}
		if got[0].Count != 9 {
			t.Errorf("count = %d, want 9", got[0].Count)
		}
		if !got[0].LastAt.Equal(at(start)) && got[0].LastAt.IsZero() {
			t.Errorf("last run = %v, want the newest run", got[0].LastAt)
		}
	})

	t.Run("limit", func(t *testing.T) {
		if got := mostFrequent(t, store, Filter{Limit: 2}); len(got) != 2 {
			t.Errorf("got %d rows, want 2: %v", len(got), got)
		}
	})
}

// A wildcard is a wildcard, and a backslash makes it literal again.
func TestLikeWildcards(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "echo 50% done", StartAt: at(1)})
	add(t, store, Entry{Cmd: "echo 50 percent", StartAt: at(2)})
	add(t, store, Entry{Cmd: "echo a_b", StartAt: at(3)})
	add(t, store, Entry{Cmd: "echo axb", StartAt: at(4)})

	if got := search(t, store, Filter{Like: `%50\%%`}); !equal(got, []string{"echo 50% done"}) {
		t.Errorf("escaped percent: got %v, want only the literal match", got)
	}
	if got := search(t, store, Filter{Like: `echo a\_b`}); !equal(got, []string{"echo a_b"}) {
		t.Errorf("escaped underscore: got %v, want only the literal match", got)
	}
	if got := search(t, store, Filter{Like: "echo a_b"}); len(got) != 2 {
		t.Errorf("unescaped underscore: got %v, want both a_b and axb", got)
	}
}

// The directory in hand wins ties, but commands from elsewhere still rank.
func TestMostFrequentPreferCwd(t *testing.T) {
	store := openTemp(t)
	for i := range 5 {
		add(t, store, Entry{Cmd: "make all", Cwd: "/elsewhere", StartAt: at(i + 1)})
	}
	add(t, store, Entry{Cmd: "make test", Cwd: "/here", StartAt: at(50)})

	got := mostFrequent(t, store, Filter{Like: "make%", PreferCwd: "/here"})
	if want := []string{"make test", "make all"}; !equal(got, want) {
		t.Errorf("got %v, want this directory first", got)
	}

	// Without the preference, frequency alone decides.
	got = mostFrequent(t, store, Filter{Like: "make%"})
	if want := []string{"make all", "make test"}; !equal(got, want) {
		t.Errorf("got %v, want frequency order", got)
	}
}

// A LIKE pattern is bound, not interpolated.
func TestSearchPatternIsNotSQL(t *testing.T) {
	store := openTemp(t)
	add(t, store, Entry{Cmd: "ls", StartAt: at(1)})

	if got := search(t, store, Filter{Like: "'; DROP TABLE history; --"}); len(got) != 0 {
		t.Fatalf("got %v, want no matches", got)
	}
	if got, want := search(t, store, Filter{}), []string{"ls"}; !equal(got, want) {
		t.Errorf("table gone: got %v, want %v", got, want)
	}
}

// Every shell opens the database for itself, so several can hit an unmigrated
// file at once.
func TestConcurrentOpenAndAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "histdb.db")
	const writers = 8

	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := Open(context.Background(), path)
			if err != nil {
				errs <- err
				return
			}
			defer store.Close()

			sess := Session{Key: fmt.Sprintf("session-%d", i), Shell: "zsh", StartAt: at(i)}
			if err := store.Sessions().Ensure(context.Background(), &sess); err != nil {
				errs <- err
				return
			}
			errs <- store.Entries().Start(context.Background(), &Entry{
				Session: sess, Cmd: fmt.Sprintf("cmd-%d", i), StartAt: at(i),
			})
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	if got := search(t, store, Filter{}); len(got) != writers {
		t.Errorf("got %d entries, want %d: %v", len(got), writers, got)
	}
}

// Commands from one shell race each other, and each resolves the session on
// its own.
func TestConcurrentEnsureSameSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "histdb.db")
	const writers = 8

	ids := make(chan int64, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := Open(context.Background(), path)
			if err != nil {
				t.Error(err)
				return
			}
			defer store.Close()

			sess := Session{Key: "shared", Shell: "zsh", StartAt: at(0)}
			if err := store.Sessions().Ensure(context.Background(), &sess); err != nil {
				t.Error(err)
				return
			}
			ids <- sess.ID
		}()
	}
	wg.Wait()
	close(ids)

	seen := map[int64]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Errorf("got %d session ids, want 1: %v", len(seen), seen)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "histdb.db")

	for i := range 3 {
		store, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		add(t, store, Entry{Cmd: "ls", StartAt: at(i)})
		store.Close()
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	if got := search(t, store, Filter{}); len(got) != 3 {
		t.Errorf("got %d entries, want 3", len(got))
	}
}
