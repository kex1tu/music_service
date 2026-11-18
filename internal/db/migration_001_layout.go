package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"regexp"
	"strings"
)

type unversionedLayout struct {
	addAlbumYouTubeID   bool
	addTrackPin         bool
	rebuildAudit        bool
	rebuildMissing      bool
	addMissingPin       bool
	addMissingUpdatedAt bool
	auditIndexes        []string
	missingIndexes      []string
}

// The original core tables are present in every supported nonempty database.
// Later releases added tables without changing those absent on older installs.
var originalTables = [...]string{
	"artists", "albums", "tracks", "genres", "track_genres",
	"missing_tracks", "audit_issues", "download_jobs",
}

func identifyUnversioned(ctx context.Context, conn *sql.Conn) (unversionedLayout, error) {
	var layout unversionedLayout
	rows, err := conn.QueryContext(ctx, `SELECT type, name, tbl_name, sql,
		CASE WHEN type = 'index' THEN (SELECT "unique" FROM pragma_index_list(tbl_name) WHERE name = sqlite_schema.name) END
		FROM sqlite_schema WHERE substr(name, 1, 7) <> 'sqlite_' ORDER BY name`)
	if err != nil {
		return layout, err
	}
	defer rows.Close()

	tables := make(map[string]string)
	for rows.Next() {
		var kind, name, table, ddl string
		var unique sql.NullBool
		if err := rows.Scan(&kind, &name, &table, &ddl, &unique); err != nil {
			return layout, err
		}
		switch {
		case kind == "table":
			tables[name] = ddl
		case kind == "index" && unique.Valid && !unique.Bool:
			// Preserve secondary indexes, even if they aren't part of schemaV1.
			switch table {
			case "audit_issues":
				layout.auditIndexes = append(layout.auditIndexes, ddl)
			case "missing_tracks":
				layout.missingIndexes = append(layout.missingIndexes, ddl)
			}
		default:
			return layout, fmt.Errorf("неизвестная схема user_version=0: неожиданный объект %s %q", kind, name)
		}
	}
	if err := rows.Err(); err != nil {
		return layout, err
	}
	if len(tables) == 0 {
		return layout, nil
	}

	for _, name := range originalTables {
		if _, ok := tables[name]; !ok {
			return layout, fmt.Errorf("неизвестная схема user_version=0: отсутствует таблица %s", name)
		}
	}

	for name, ddl := range tables {
		expected := tablesV1[name]
		if expected == nil {
			return layout, fmt.Errorf("неизвестная схема user_version=0: неизвестная таблица %q", name)
		}
		actual := tableClauses(ddl)
		switch name {
		case "albums":
			// The audited local layout predates this nullable column.
			layout.addAlbumYouTubeID = !actual["youtube_id text"]
			if layout.addAlbumYouTubeID {
				actual["youtube_id text"] = true
			}
		case "tracks":
			layout.addTrackPin = !actual["pinned_source_url text"]
			if layout.addTrackPin {
				actual["pinned_source_url text"] = true
			}
			replaceClause(actual, "lyrics_status text check ( lyrics_status in ( 'none' , 'lrc' , 'txt' ) ) default 'none'", "lyrics_status text default 'none'")
		case "audit_issues":
			if maps.Equal(actual, legacyAuditClauses) {
				layout.rebuildAudit = true
				continue
			}
		case "missing_tracks":
			layout.rebuildMissing = !maps.Equal(actual, expected)
			layout.addMissingPin = !actual["pinned_source_url text"]
			layout.addMissingUpdatedAt = !actual["updated_at datetime default current_timestamp"] && !actual["updated_at datetime"]
			if layout.addMissingPin {
				actual["pinned_source_url text"] = true
			}
			if layout.addMissingUpdatedAt {
				actual["updated_at datetime default current_timestamp"] = true
			}
			replaceClause(actual, "updated_at datetime", "updated_at datetime default current_timestamp")
			replaceClause(actual, "expected_duration real", "expected_duration integer")
			replaceClause(actual, "status text check ( status in ( 'missing' , 'queued' , 'downloaded' , 'ignored' ) ) default 'missing'", "status text default 'missing'")
			replaceClause(actual, "unique ( album_id , disc_number , track_number )", "unique ( album_id , track_number , disc_number )")
		}
		if !maps.Equal(actual, expected) {
			return layout, fmt.Errorf("неизвестная схема user_version=0: неподдерживаемые колонки или ограничения таблицы %s", name)
		}
	}
	return layout, nil
}

func replaceClause(clauses map[string]bool, old, replacement string) {
	if clauses[old] {
		delete(clauses, old)
		clauses[replacement] = true
	}
}

var tablesV1 = func() map[string]map[string]bool {
	tables := make(map[string]map[string]bool)
	for _, statement := range strings.Split(schemaV1, "CREATE TABLE IF NOT EXISTS ")[1:] {
		ddl, _, _ := strings.Cut(statement, "\n);")
		name, _, _ := strings.Cut(ddl, " ")
		tables[name] = tableClauses(ddl + ")")
	}
	return tables
}()

// Compare whole column and constraint clauses, including CHECKs, defaults and
// foreign keys. SQLite can move appended columns within the stored CREATE SQL.
var ddlToken = regexp.MustCompile(`--[^\n]*|/\*(?s:.*?)\*/|'(?:''|[^'])*'|[A-Za-z0-9_]+|[^[:space:]]`)

func tableClauses(ddl string) map[string]bool {
	_, body, _ := strings.Cut(ddl, "(")
	clauses := make(map[string]bool)
	var clause []string
	depth := 1
	for _, token := range ddlToken.FindAllString(body, -1) {
		if strings.HasPrefix(token, "--") || strings.HasPrefix(token, "/*") {
			continue
		}
		if token == ")" {
			depth--
		}
		if (depth == 1 && token == ",") || (depth == 0 && token == ")") {
			clauses[strings.Join(clause, " ")] = true
			clause = clause[:0]
		} else {
			if !strings.HasPrefix(token, "'") {
				token = strings.ToLower(token)
			}
			clause = append(clause, token)
		}
		if token == "(" {
			depth++
		}
	}
	// Table options such as STRICT and WITHOUT ROWID affect compatibility.
	if len(clause) > 0 {
		clauses[strings.Join(clause, " ")] = true
	}
	return clauses
}

var legacyAuditClauses = tableClauses(`CREATE TABLE audit_issues (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	entity_type TEXT CHECK(entity_type IN ('track', 'album', 'genre', 'file')) NOT NULL,
	entity_id INTEGER NOT NULL,
	issue_type TEXT CHECK(issue_type IN ('invalid_path', 'non_en_genre', 'missing_lyrics',
		'missing_cover', 'missing_track', 'duplicate', 'corrupt_file')) NOT NULL,
	severity TEXT CHECK(severity IN ('error', 'warning', 'info')) DEFAULT 'warning',
	description TEXT NOT NULL,
	details JSON,
	suggested_action TEXT,
	is_auto_fixed BOOLEAN DEFAULT 0,
	is_resolved BOOLEAN DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`)
