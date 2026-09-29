package db

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func Open(databaseURL string) (*sql.DB, error) {
	d, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	if err = d.Ping(); err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(20)
	return d, nil
}

// Migrate 按文件名顺序执行内嵌 SQL（幂等，可重复启动）
func Migrate(d *sql.DB) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		content, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := d.Exec(string(content)); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
	}
	return nil
}
