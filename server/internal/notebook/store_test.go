package notebook

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "notes.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}
func create(t *testing.T, s *Store, key, title, body string) Record {
	t.Helper()
	r, e := s.Mutate(t.Context(), "agent-a", "create", Mutation{Key: key, Title: title, Body: body, Tags: []string{"печать"}, OperationID: "create-" + key})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestLifecycleConcurrentWritesAndReplay(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	r := create(t, s, "workshop/printer", "Принтер", "Адрес: 192.0.2.1")
	create(t, s, "tools/slicer", "Слайсер", "/opt/OrcaSlicer --profile /profiles/PLA.json")
	items, e := s.Read(ctx, []string{r.Key, "tools/slicer"})
	if e != nil || len(items) != 2 {
		t.Fatalf("read: %v %v", items, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			body := fmt.Sprintf("Адрес: 192.0.2.%d", i+2)
			_, e := s.Mutate(ctx, fmt.Sprintf("agent-%d", i), "update", Mutation{Key: r.Key, ExpectedRevision: 1, Changes: &Changes{Body: &body}, OperationID: "change"})
			errs <- e
		})
	}
	wg.Wait()
	close(errs)
	succeeded := 0
	for e := range errs {
		if e == nil {
			succeeded++
		} else {
			wantCode(t, e, "revision_conflict")
		}
	}
	if succeeded != 1 {
		t.Fatalf("successes %d", succeeded)
	}
	items, _ = s.Read(ctx, []string{r.Key})
	if items[0].Revision != 2 {
		t.Fatal(items)
	}
	body := "Адрес: 192.0.2.50"
	m := Mutation{Key: r.Key, ExpectedRevision: 2, Changes: &Changes{Body: &body}, OperationID: "retry"}
	updated, e := s.Mutate(ctx, "agent-a", "update", m)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Mutate(ctx, "agent-a", "update", m)
	if e != nil || !reflect.DeepEqual(updated, again) {
		t.Fatalf("replay %v %v", again, e)
	}
	other := "different"
	m.Changes = &Changes{Body: &other}
	_, e = s.Mutate(ctx, "agent-a", "update", m)
	wantCode(t, e, "operation_conflict")
	_, e = s.Mutate(ctx, "agent-a", "delete", Mutation{Key: r.Key, ExpectedRevision: 2, OperationID: "stale-delete"})
	wantCode(t, e, "revision_conflict")
	deleted, e := s.Mutate(ctx, "agent-a", "delete", Mutation{Key: r.Key, ExpectedRevision: 3, OperationID: "delete"})
	if e != nil || !deleted.Deleted {
		t.Fatalf("delete %v %v", deleted, e)
	}
	_, e = s.Mutate(ctx, "agent-b", "create", Mutation{Key: r.Key, Title: "replacement", OperationID: "replace"})
	wantCode(t, e, "key_exists")
	search, e := s.Search(ctx, Search{Query: "Принтер"})
	if e != nil || len(search.Items) != 0 {
		t.Fatalf("deleted found: %v %v", search, e)
	}
	search, e = s.Search(ctx, Search{Query: "Принтер", Deleted: true})
	if e != nil || len(search.Items) != 1 {
		t.Fatalf("trash %v %v", search, e)
	}
	restored, e := s.Mutate(ctx, "agent-b", "restore", Mutation{Key: r.Key, ExpectedRevision: 4, Revision: 1, OperationID: "restore"})
	if e != nil || restored.Body != r.Body || restored.Deleted || restored.Revision != 5 {
		t.Fatalf("restore %v %v", restored, e)
	}
	// Old successful receipts remain valid even after subsequent writes.
	m.Changes = &Changes{Body: &body}
	again, e = s.Mutate(ctx, "agent-a", "update", m)
	if e != nil || again.Revision != 3 {
		t.Fatalf("old replay %v %v", again, e)
	}
}

func TestSearchRussianIdentifiersPaginationAndLimits(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	create(t, s, "workshop/printer", "ПРИНТЕР мастерской", "Настройка принтера. Путь /opt/OrcaSlicer; адрес 192.0.2.50. 100%_done")
	create(t, s, "tools/slicer", "Слайсер", "Профиль принтера в мастерской /opt/OrcaSlicer")
	cases := []struct {
		q, mode string
		count   int
	}{{"принтер", "text", 1}, {"принтер*", "text", 2}, {"принтер* мастерской", "text", 2}, {"принтер неизвестный", "text", 0}, {"/opt/OrcaSlicer", "literal", 2}, {"/opt/orcaslicer", "literal", 0}, {"192.0.2.50", "literal", 1}, {"100%_", "literal", 1}, {"OR NOT", "text", 0}}
	for _, c := range cases {
		t.Run(c.q+c.mode, func(t *testing.T) {
			r, e := s.Search(ctx, Search{Query: c.q, Mode: c.mode})
			if e != nil || len(r.Items) != c.count {
				t.Fatalf("got %v, %v", r, e)
			}
			for _, v := range r.Items {
				if len([]rune(v.Snippet)) > 322 {
					t.Fatal("unbounded snippet")
				}
			}
		})
	}
	r, e := s.Search(ctx, Search{Query: "принтер*", Prefix: "tools/", Tags: []string{"печать"}})
	if e != nil || len(r.Items) != 1 || r.Items[0].Key != "tools/slicer" {
		t.Fatalf("filters %v %v", r, e)
	}
	r, e = s.Search(ctx, Search{Limit: 1})
	if e != nil || r.NextCursor == "" {
		t.Fatal(r, e)
	}
	second, e := s.Search(ctx, Search{Limit: 1, Cursor: r.NextCursor})
	if e != nil || len(second.Items) != 1 || second.Items[0].Key == r.Items[0].Key || second.NextCursor != "" {
		t.Fatal(second, e)
	}
	_, e = s.Search(ctx, Search{Limit: 2, Cursor: r.NextCursor})
	wantCode(t, e, "invalid_request")
	create(t, s, "a/new", "New", "new")
	_, e = s.Search(ctx, Search{Limit: 1, Cursor: r.NextCursor})
	wantCode(t, e, "stale_cursor")
	_, e = s.Search(ctx, Search{Limit: 51})
	wantCode(t, e, "invalid_request")
	_, e = s.Search(ctx, Search{Query: "*"})
	wantCode(t, e, "invalid_request")
}

func TestRestartExportRestoreHistoryAndAccessIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.sqlite")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	r := create(t, s, "tools/slicer", "Слайсер", "Первоначальная команда")
	ctx := t.Context()
	if e = s.SetPermission(ctx, "agent-a", Permission{Write: true}); e != nil {
		t.Fatal(e)
	}
	for i := range HistoryLimit + 2 {
		body := fmt.Sprintf("command %d", i)
		r, e = s.Mutate(ctx, "agent-a", "update", Mutation{Key: r.Key, ExpectedRevision: r.Revision, Changes: &Changes{Body: &body}, OperationID: fmt.Sprint(i)})
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, e := s.Permission(ctx, "agent-a")
	if e != nil || !p.Write || !p.Read {
		t.Fatal(p, e)
	}
	items, e := s.Read(ctx, []string{r.Key})
	if e != nil || !reflect.DeepEqual(items[0], r) {
		t.Fatal(items, e)
	}
	history, e := s.History(ctx, r.Key, 0)
	if e != nil || len(history.([]Revision)) != HistoryLimit {
		t.Fatal(history, e)
	}
	_, e = s.History(ctx, r.Key, 1)
	wantCode(t, e, "not_found")
	exported, e := s.Export(ctx)
	if e != nil {
		t.Fatal(e)
	}
	bytes, e := json.Marshal(exported)
	if e != nil {
		t.Fatal(e)
	}
	var transfer Export
	if e = json.Unmarshal(bytes, &transfer); e != nil {
		t.Fatal(e)
	}
	destination := testStore(t)
	if e = destination.Import(ctx, transfer); e != nil {
		t.Fatal(e)
	}
	copy, e := destination.Export(ctx)
	if e != nil || !reflect.DeepEqual(copy.Records, exported.Records) || !reflect.DeepEqual(copy.History, exported.History) {
		t.Fatal("restore differs", e)
	}
	p, e = destination.Permission(ctx, "agent-a")
	if e != nil || p.Read || p.Write {
		t.Fatal("permissions must not be exported", p, e)
	}
	wantCode(t, destination.Import(ctx, transfer), "import_conflict")
	broken := transfer
	broken.History = broken.History[1:]
	empty := testStore(t)
	wantCode(t, empty.Import(ctx, broken), "invalid_request")
	got, _ := empty.Export(ctx)
	if len(got.Records) != 0 {
		t.Fatal("partial import")
	}
}

func TestRejectedWritesAreAtomicAndExpiredReceiptsDoNotOverwrite(t *testing.T) {
	s := testStore(t)
	r := create(t, s, "test/item", "Item", "original")
	ctx := t.Context()
	tooLarge := strings.Repeat("x", MaxBodyBytes+1)
	_, e := s.Mutate(ctx, "agent-a", "update", Mutation{Key: r.Key, ExpectedRevision: 1, Changes: &Changes{Body: &tooLarge}, OperationID: "bad"})
	wantCode(t, e, "invalid_request")
	items, _ := s.Read(ctx, []string{r.Key})
	if items[0].Revision != 1 || items[0].Body != "original" {
		t.Fatal(items)
	}
	body := "new"
	m := Mutation{Key: r.Key, ExpectedRevision: 1, Changes: &Changes{Body: &body}, OperationID: "ok"}
	if _, e = s.Mutate(ctx, "agent-a", "update", m); e != nil {
		t.Fatal(e)
	}
	_, e = s.db.Exec("UPDATE operations SET created_at=?", time.Now().Add(-OperationRetention-time.Hour).Unix())
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Mutate(ctx, "agent-a", "update", m)
	wantCode(t, e, "revision_conflict")
}

func TestStorageFailureRollsBackSnapshotHistoryAndReceipt(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	_, err := s.db.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'simulated storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	m := Mutation{Key: "tools/slicer", Title: "Slicer", Body: "command", OperationID: "first"}
	if _, err = s.Mutate(ctx, "agent-a", "create", m); err == nil {
		t.Fatal("expected storage failure")
	}
	out, err := s.Export(ctx)
	if err != nil || len(out.Records) != 0 || len(out.History) != 0 {
		t.Fatal("partial write", out, err)
	}
	if _, err = s.db.Exec("DROP TRIGGER reject_receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(ctx, "agent-a", "create", m); err != nil {
		t.Fatal("retry after rollback", err)
	}
}

func TestSeparateConnectionsAndRestartedRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.sqlite")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(path)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	defer b.Close()
	create(t, a, "tools/slicer", "Slicer", "old")
	body := "new"
	m := Mutation{Key: "tools/slicer", ExpectedRevision: 1, Changes: &Changes{Body: &body}, OperationID: "same"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Go(func() { _, e := s.Mutate(t.Context(), fmt.Sprint(i), "update", m); errs <- e })
	}
	wg.Wait()
	close(errs)
	count := 0
	for e := range errs {
		if e == nil {
			count++
		} else {
			wantCode(t, e, "revision_conflict")
		}
	}
	if count != 1 {
		t.Fatal(count)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	items, err := a.Read(t.Context(), []string{m.Key})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Mutate(t.Context(), items[0].UpdatedBy, "update", m)
	if err != nil || got.Revision != 2 {
		t.Fatal(got, err)
	}
}

func TestDatabaseBoundaryAndBuildFeatures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("new database is not private", info.Mode())
	}
	var fts int
	if err = s.db.QueryRow("SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	t.Logf("current SQLite build ENABLE_FTS5=%d; notebook text/literal search requires no FTS extension", fts)
	other := filepath.Join(t.TempDir(), "events.db")
	db, err := sql.Open("sqlite3", other)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE events(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if wrong, err := Open(other); err == nil {
		wrong.Close()
		t.Fatal("must reject unrelated database")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='notes'").Scan(&count); err != nil || count != 0 {
		t.Fatal("modified unrelated schema", count, err)
	}
}

func TestTagNormalizationDoesNotChangeRetryInput(t *testing.T) {
	s := testStore(t)
	m := Mutation{Key: "tools/slicer", Title: "Slicer", Tags: []string{"z", "a", "a"}, OperationID: "tags"}
	r, err := s.Mutate(t.Context(), "agent-a", "create", m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Tags, []string{"z", "a", "a"}) || !reflect.DeepEqual(r.Tags, []string{"a", "z"}) {
		t.Fatal("mutated caller or failed to normalize", m.Tags, r.Tags)
	}
	again, err := s.Mutate(t.Context(), "agent-a", "create", m)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal(again, err)
	}
}
