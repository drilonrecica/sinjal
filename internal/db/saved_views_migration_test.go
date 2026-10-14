package db

import "testing"

// 010_saved_views: per-user names are unique and views go with the user.
func TestSavedViewsMigration(t *testing.T) {
	e := migratedEnv(t)
	e.mustExec(t, `INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u1', 'u1', 'viewer', 'x', 'x')`)
	e.mustExec(t, `INSERT INTO users (id, login, role, created_at, updated_at) VALUES ('u2', 'u2', 'viewer', 'x', 'x')`)
	ins := `INSERT INTO saved_views (id, user_id, name, filters_json, created_at, updated_at) VALUES (?, ?, ?, '{}', 'x', 'x')`
	e.mustExec(t, ins, "a", "u1", "Infra")
	e.mustExec(t, ins, "b", "u2", "Infra")
	if err := e.exec(t, ins, "c", "u1", "Infra"); err == nil {
		t.Error("a user's view name is not unique")
	}
	e.mustExec(t, `DELETE FROM users WHERE id = 'u1'`)
	if n := e.count(t, `SELECT COUNT(*) FROM saved_views`); n != 1 {
		t.Errorf("%d views after the user went, want 1", n)
	}
}
