package radio

import (
	"context"
	"database/sql"
)

// DBTX определяет контракт выполнения SQL-запросов.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository определяет методы доступа к данным медиатеки для радио.
type Repository interface {
	GetAvailableGenres(ctx context.Context, limit int) ([]string, error)
}

// SQLiteRepository реализует Repository поверх базы SQLite.
type SQLiteRepository struct {
	db DBTX
}

// NewRepository создает новый экземпляр репозитория Radio.
func NewRepository(db DBTX) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// GetAvailableGenres возвращает топ жанров медиатеки по популярности.
func (r *SQLiteRepository) GetAvailableGenres(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	query := `
	SELECT g.name
	FROM genres g
	JOIN track_genres tg ON tg.genre_id = g.id
	GROUP BY g.id, g.name
	ORDER BY COUNT(tg.track_id) DESC
	LIMIT ?;
	`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil && name != "" {
			result = append(result, name)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}
