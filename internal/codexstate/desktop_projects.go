package codexstate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/threadcatalogcontract"
)

type desktopGlobalProject struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	RootPaths []string `json:"rootPaths"`
}

type desktopGlobalState struct {
	Projects    map[string]desktopGlobalProject `json:"local-projects"`
	Assignments map[string]struct {
		Kind string `json:"projectKind"`
		ID   string `json:"projectId"`
	} `json:"thread-project-assignments"`
	Projectless []string                     `json:"projectless-thread-ids"`
	LegacyIDs   map[string]map[string]string `json:"app-server-project-id-by-legacy-project-id-by-host"`
}

// DesktopProjects reads a fresh, metadata-only snapshot. Desktop display groups
// do not replace the workspace paths used for execution and ownership checks.
func (c *SQLiteThreadCatalog) DesktopProjects() (threadcatalogcontract.DesktopProjectSnapshot, error) {
	result := threadcatalogcontract.DesktopProjectSnapshot{ThreadProjectIDs: map[string]string{}}
	if c == nil || strings.TrimSpace(c.path) == "" {
		return result, fmt.Errorf("missing codex sqlite path")
	}
	global, globalAvailable, err := readDesktopGlobalState(filepath.Join(filepath.Dir(c.path), ".codex-global-state.json"))
	if err != nil {
		return result, err
	}
	var sqlProjects, sqlRoots bool
	err = c.readWithRetry("query desktop projects", func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Rebuild all output on retry rather than appending a partial snapshot.
		result.Projects = nil
		result.ThreadProjectIDs = map[string]string{}
		sqlProjects, sqlRoots, err = readDesktopSQLProjects(tx, &result)
		if err != nil {
			return err
		}
		if err := readDesktopSQLAssignments(tx, result.ThreadProjectIDs); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return result, err
	}
	result.Available = sqlProjects || globalAvailable
	if !sqlProjects {
		for id, project := range global.Projects {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			result.Projects = append(result.Projects, threadcatalogcontract.DesktopProject{
				ID: id, Name: strings.TrimSpace(project.Name), RootPaths: cleanDesktopRoots(project.RootPaths),
			})
		}
		sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].ID < result.Projects[j].ID })
	}
	known := make(map[string]bool, len(result.Projects))
	for _, project := range result.Projects {
		known[project.ID] = true
	}
	if sqlProjects && !sqlRoots {
		for i := range result.Projects {
			project := &result.Projects[i]
			for legacyID, legacy := range global.Projects {
				if resolveDesktopProjectID(legacyID, known, global.LegacyIDs) == project.ID {
					project.RootPaths = cleanDesktopRoots(append(project.RootPaths, legacy.RootPaths...))
				}
			}
		}
	}
	sqlAssigned := make(map[string]bool, len(result.ThreadProjectIDs))
	for threadID := range result.ThreadProjectIDs {
		sqlAssigned[threadID] = true
	}
	for threadID, assignment := range global.Assignments {
		threadID = strings.TrimSpace(threadID)
		if threadID == "" || sqlAssigned[threadID] {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(assignment.Kind)) {
		case "local":
			// Even a deleted or ambiguous explicit assignment must suppress CWD inference.
			result.ThreadProjectIDs[threadID] = resolveDesktopProjectID(assignment.ID, known, global.LegacyIDs)
		case "cloud", "chatgpt":
			// A cloud assignment must not be guessed into a local project by CWD.
			result.ThreadProjectIDs[threadID] = ""
		}
	}
	for _, threadID := range global.Projectless {
		threadID = strings.TrimSpace(threadID)
		if threadID != "" && !sqlAssigned[threadID] {
			result.ThreadProjectIDs[threadID] = ""
		}
	}
	return result, nil
}

func readDesktopGlobalState(path string) (desktopGlobalState, bool, error) {
	var global desktopGlobalState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return global, false, nil
	}
	if err != nil {
		return global, false, fmt.Errorf("read desktop project state: %w", err)
	}
	if err := json.Unmarshal(data, &global); err != nil {
		return global, false, fmt.Errorf("decode desktop project state: %w", err)
	}
	// Nil means the key is absent. An explicitly empty project map is available.
	return global, global.Projects != nil || global.Assignments != nil || global.Projectless != nil, nil
}

func resolveDesktopProjectID(id string, known map[string]bool, mappings map[string]map[string]string) string {
	id = strings.TrimSpace(id)
	if known[id] {
		return id
	}
	resolved := ""
	for _, byLegacy := range mappings {
		candidate := strings.TrimSpace(byLegacy[id])
		if !known[candidate] {
			continue
		}
		if resolved != "" && resolved != candidate {
			return "" // Host-specific ambiguity cannot safely select a local project.
		}
		resolved = candidate
	}
	return resolved
}

func cleanDesktopRoots(roots []string) []string {
	var cleaned []string
	seen := map[string]bool{}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root != "" && !seen[root] {
			seen[root] = true
			cleaned = append(cleaned, root)
		}
	}
	return cleaned
}

func desktopTableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	// All callers supply fixed table names, never user input.
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func readDesktopSQLProjects(tx *sql.Tx, result *threadcatalogcontract.DesktopProjectSnapshot) (bool, bool, error) {
	columns, err := desktopTableColumns(tx, "projects")
	if err != nil || !columns["id"] || !columns["name"] {
		return false, false, err
	}
	order := "id"
	if columns["position"] {
		order = "position, id"
	}
	rows, err := tx.Query("SELECT id, name FROM projects ORDER BY " + order)
	if err != nil {
		return false, false, err
	}
	for rows.Next() {
		var id, name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return false, false, err
		}
		if strings.TrimSpace(id.String) != "" {
			result.Projects = append(result.Projects, threadcatalogcontract.DesktopProject{ID: strings.TrimSpace(id.String), Name: strings.TrimSpace(name.String)})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, false, err
	}
	columns, err = desktopTableColumns(tx, "project_roots")
	if err != nil || !columns["project_id"] || !columns["path"] {
		return true, false, err
	}
	order = "project_id, path"
	if columns["position"] {
		order = "project_id, position, path"
	}
	roots, err := tx.Query("SELECT project_id, path FROM project_roots ORDER BY " + order)
	if err != nil {
		return true, false, err
	}
	defer roots.Close()
	index := map[string]int{}
	for i, project := range result.Projects {
		index[project.ID] = i
	}
	for roots.Next() {
		var id, path sql.NullString
		if err := roots.Scan(&id, &path); err != nil {
			return true, false, err
		}
		if i, ok := index[strings.TrimSpace(id.String)]; ok {
			result.Projects[i].RootPaths = append(result.Projects[i].RootPaths, path.String)
		}
	}
	for i := range result.Projects {
		result.Projects[i].RootPaths = cleanDesktopRoots(result.Projects[i].RootPaths)
	}
	return true, true, roots.Err()
}

func readDesktopSQLAssignments(tx *sql.Tx, assignments map[string]string) error {
	columns, err := desktopTableColumns(tx, "threads")
	if err != nil || !columns["id"] || !columns["project_id"] {
		return err
	}
	rows, err := tx.Query("SELECT id, project_id FROM threads WHERE project_id IS NOT NULL AND TRIM(project_id) <> ''")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID string
		if err := rows.Scan(&id, &projectID); err != nil {
			return err
		}
		if id = strings.TrimSpace(id); id != "" {
			assignments[id] = strings.TrimSpace(projectID)
		}
	}
	return rows.Err()
}
