package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestRecentReleasedDatabaseFixtures(t *testing.T) {
	for _, version := range []int{24, 26} {
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "released.db")
			copyDatabaseFixture(t, path, fmt.Sprintf("testdata/released-v%d.db", version))
			raw := openRawDatabase(t, path)
			assertPragmaInt(t, raw, "user_version", version)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			assertPragmaInt(t, db, "user_version", LatestSchemaVersion)
			assertQueryInt(t, db, `SELECT count(*) FROM pellets WHERE title='Release upgrade preserved task'
				AND description='# Requirements'||char(10)||'Preserve Unicode: café'
				AND external_id='release-upgrade-1' AND status='open'`, 1)
			assertQueryInt(t, db, `SELECT count(*) FROM memories WHERE text='Release upgrade preserved memory'`, 1)
			assertQueryInt(t, db, `SELECT count(*) FROM pellets_fts WHERE pellets_fts MATCH 'preserved'`, 1)
			assertQueryInt(t, db, `SELECT count(*) FROM memories_fts WHERE memories_fts MATCH 'preserved'`, 1)
			if version == 24 {
				assertQueryInt(t, db, `SELECT count(*) FROM pellets WHERE model IS NULL AND reasoning_effort IS NULL`, 1)
			} else {
				assertQueryInt(t, db, `SELECT count(*) FROM pellets WHERE model='custom-model' AND reasoning_effort='high'`, 1)
			}
		})
	}
}
