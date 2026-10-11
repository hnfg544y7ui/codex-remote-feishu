package codexstate

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/threadcatalogcontract"
)

func TestDesktopProjectsFreshSQLAndLegacyAssignments(t *testing.T) {
	path := desktopProjectTestDB(t, `
CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT);
CREATE TABLE project_roots (project_id TEXT, position INTEGER, path TEXT);
CREATE TABLE threads (id TEXT PRIMARY KEY, project_id TEXT);
INSERT INTO projects VALUES ('native-a', '中文项目'), ('native-b', '第二项目');
INSERT INTO project_roots VALUES ('native-a', 0, '/a'), ('native-a', 1, '/extra');
INSERT INTO threads VALUES ('sql-wins', 'native-a'), ('legacy', NULL), ('unknown', NULL);`)
	writeDesktopProjectGlobal(t, path, `{
"local-projects":{"legacy-a":{"name":"旧名","rootPaths":["/a","/deleted"]}},
"app-server-project-id-by-legacy-project-id-by-host":{"host-a":{"legacy-a":"native-a"},"host-b":{"legacy-a":"unrelated"}},
"thread-project-assignments":{"legacy":{"projectKind":"local","projectId":"legacy-a"},"sql-wins":{"projectKind":"local","projectId":"native-b"},"manual-none":{"projectKind":"local","projectId":"legacy-a"},"cloud":{"projectKind":"cloud","projectId":"native-a"}},
"projectless-thread-ids":["manual-none","sql-wins"]}`)
	catalog := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{})
	snapshot, err := catalog.DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || len(snapshot.Projects) != 2 {
		t.Fatalf("unexpected projects: %#v", snapshot)
	}
	if got := snapshot.Projects[0]; got.Name != "中文项目" || !reflect.DeepEqual(got.RootPaths, []string{"/a", "/extra"}) {
		t.Fatalf("SQL project name and complete roots must be authoritative: %#v", got)
	}
	wantAssignments := map[string]string{"sql-wins": "native-a", "legacy": "native-a", "manual-none": "", "cloud": ""}
	if !reflect.DeepEqual(snapshot.ThreadProjectIDs, wantAssignments) {
		t.Fatalf("assignments = %#v; want %#v", snapshot.ThreadProjectIDs, wantAssignments)
	}
	execDesktopProjectSQL(t, path, `UPDATE projects SET name='已改名' WHERE id='native-a'; UPDATE threads SET project_id='native-b' WHERE id='legacy'; DELETE FROM project_roots WHERE path='/extra';`)
	updated, err := catalog.DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Projects[0].Name != "已改名" || !reflect.DeepEqual(updated.Projects[0].RootPaths, []string{"/a"}) || updated.ThreadProjectIDs["legacy"] != "native-b" {
		t.Fatalf("next read must reflect rename, move and root removal: %#v", updated)
	}
}

func TestDesktopProjectsLegacySchemaAndGlobalRefresh(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE threads (id TEXT);`)
	writeDesktopProjectGlobal(t, path, `{"local-projects":{"local-a":{"name":"本地项目","rootPaths":[" /one ","/one","/two"]}},"thread-project-assignments":{"thread":{"projectKind":"local","projectId":"local-a"}}}`)
	catalog := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{})
	snapshot, err := catalog.DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || len(snapshot.Projects) != 1 || snapshot.Projects[0].Name != "本地项目" || !reflect.DeepEqual(snapshot.Projects[0].RootPaths, []string{"/one", "/two"}) || snapshot.ThreadProjectIDs["thread"] != "local-a" {
		t.Fatalf("legacy fallback: %#v", snapshot)
	}
	writeDesktopProjectGlobal(t, path, `{"local-projects":{"local-a":{"name":"新名","rootPaths":["/two"]}},"projectless-thread-ids":["thread"]}`)
	snapshot, err = catalog.DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Projects[0].Name != "新名" || !reflect.DeepEqual(snapshot.Projects[0].RootPaths, []string{"/two"}) {
		t.Fatalf("global state must refresh: %#v", snapshot)
	}
	if value, exists := snapshot.ThreadProjectIDs["thread"]; !exists || value != "" {
		t.Fatalf("explicit projectless assignment lost: %#v", snapshot.ThreadProjectIDs)
	}
}

func TestDesktopProjectsAbsentMetadataUnavailable(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE threads (id TEXT);`)
	catalog := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{})
	for _, state := range []string{"", `{"electron-saved-workspace-roots":["/old"],"electron-workspace-root-labels":{"/old":"旧目录"}}`} {
		if state != "" {
			writeDesktopProjectGlobal(t, path, state)
		}
		snapshot, err := catalog.DesktopProjects()
		if err != nil || snapshot.Available || len(snapshot.Projects) != 0 {
			t.Fatalf("directory history cannot invent desktop projects: %#v, %v", snapshot, err)
		}
	}
}

func TestDesktopProjectsEmptySQLProjectsAreAuthoritative(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE projects (id TEXT, name TEXT); CREATE TABLE threads (id TEXT);`)
	writeDesktopProjectGlobal(t, path, `{"local-projects":{"deleted":{"name":"已删除","rootPaths":["/deleted"]}}}`)
	snapshot, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects()
	if err != nil || !snapshot.Available || len(snapshot.Projects) != 0 {
		t.Fatalf("empty SQL project list must not restore stale global projects: %#v, %v", snapshot, err)
	}
}

func TestDesktopProjectsMissingColumnsAndRootsCompatibility(t *testing.T) {
	for _, schema := range []string{
		`CREATE TABLE projects (id TEXT); CREATE TABLE threads (id TEXT);`,
		`CREATE TABLE projects (id TEXT, name TEXT); INSERT INTO projects VALUES ('native-a','新项目'); CREATE TABLE project_roots (project_id TEXT); CREATE TABLE threads (id TEXT);`,
	} {
		t.Run(schema, func(t *testing.T) {
			path := desktopProjectTestDB(t, schema)
			writeDesktopProjectGlobal(t, path, `{"local-projects":{"legacy-a":{"name":"遗留项目","rootPaths":["/one","/two"]}},"app-server-project-id-by-legacy-project-id-by-host":{"host":{"legacy-a":"native-a"}}}`)
			snapshot, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects()
			if err != nil || !snapshot.Available || len(snapshot.Projects) != 1 || !reflect.DeepEqual(snapshot.Projects[0].RootPaths, []string{"/one", "/two"}) {
				t.Fatalf("compatible roots fallback: %#v, %v", snapshot, err)
			}
		})
	}
}

func TestDesktopProjectsAmbiguousAndDeletedAssignmentsSuppressInference(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE projects (id TEXT, name TEXT); INSERT INTO projects VALUES ('one','一'),('two','二');`)
	writeDesktopProjectGlobal(t, path, `{"thread-project-assignments":{"thread":{"projectKind":"local","projectId":"legacy"},"deleted":{"projectKind":"local","projectId":"deleted-project"}},"app-server-project-id-by-legacy-project-id-by-host":{"host-one":{"legacy":"one"},"host-two":{"legacy":"two"}}}`)
	snapshot, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"thread", "deleted"} {
		if got, exists := snapshot.ThreadProjectIDs[id]; !exists || got != "" {
			t.Fatalf("explicit unresolved assignment must suppress inference: %#v", snapshot.ThreadProjectIDs)
		}
	}
}

func TestDesktopProjectsPreservesSQLProjectPosition(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE projects (id TEXT, name TEXT, position INTEGER); INSERT INTO projects VALUES ('a','后面',2),('z','前面',0);`)
	snapshot, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Projects) != 2 || snapshot.Projects[0].ID != "z" || snapshot.Projects[1].ID != "a" {
		t.Fatalf("project order must follow desktop position, not ID: %#v", snapshot.Projects)
	}
}

func TestDesktopProjectsCorruptMetadataReturnsError(t *testing.T) {
	path := desktopProjectTestDB(t, `CREATE TABLE threads (id TEXT);`)
	writeDesktopProjectGlobal(t, path, `{"local-projects":`)
	if _, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects(); err == nil {
		t.Fatal("corrupt JSON must return an error instead of pretending projects are absent")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(path), ".codex-global-state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLiteThreadCatalog(path, SQLiteThreadCatalogOptions{}).DesktopProjects(); err == nil {
		t.Fatal("non-schema SQLite failure must not become a compatibility fallback")
	}
}

var _ threadcatalogcontract.DesktopProjectCatalog = (*SQLiteThreadCatalog)(nil)

func desktopProjectTestDB(t *testing.T, schema string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state_5.sqlite")
	execDesktopProjectSQL(t, path, schema)
	return path
}

func execDesktopProjectSQL(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func writeDesktopProjectGlobal(t *testing.T, dbPath, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), ".codex-global-state.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
