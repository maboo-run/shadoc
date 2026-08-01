package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var (
	foreignKeyActionPattern = `(?:\s+(?:ON\s+(?:DELETE|UPDATE)\s+(?:SET\s+(?:NULL|DEFAULT)|CASCADE|RESTRICT|NO\s+ACTION)|MATCH\s+[A-Za-z_][A-Za-z0-9_]*|NOT\s+DEFERRABLE|DEFERRABLE(?:\s+INITIALLY\s+(?:DEFERRED|IMMEDIATE))?))*`
	foreignKeyTablePattern  = `(?:"[^"]+"|\[[^\]]+\]|[A-Za-z_][A-Za-z0-9_]*)`
	tableForeignKeyPattern  = regexp.MustCompile(`(?i),\s*(?:CONSTRAINT\s+` + foreignKeyTablePattern + `\s+)?FOREIGN\s+KEY\s*\([^)]*\)\s+REFERENCES\s+` + foreignKeyTablePattern + `\s*\([^)]*\)` + foreignKeyActionPattern)
	inlineReferencePattern  = regexp.MustCompile(`(?i)\s+REFERENCES\s+` + foreignKeyTablePattern + `\s*\([^)]*\)` + foreignKeyActionPattern)
)

// ensureLogicalReferencesOnly removes historical SQLite foreign-key clauses.
// Relationships are validated and cleaned up by store transactions so deletion
// behavior is explicit and consistent for both new and upgraded databases.
func (s *Store) ensureLogicalReferencesOnly(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable SQLite foreign keys: %w", err)
	}
	tables, err := hardForeignKeyTables(ctx, s.db)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range tables {
		if err := rebuildWithoutForeignKeys(ctx, tx, table); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit logical-reference migration: %w", err)
	}
	remaining, err := hardForeignKeyTables(ctx, s.db)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("logical-reference migration left hard foreign keys on %s", strings.Join(remaining, ", "))
	}
	return nil
}

type schemaArtifact struct {
	name string
	sql  string
}

func rebuildWithoutForeignKeys(ctx context.Context, tx *sql.Tx, table string) error {
	var definition string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&definition); err != nil {
		return fmt.Errorf("read schema for %s: %w", table, err)
	}
	sequence, hasSequence, err := sqliteSequence(ctx, tx, table)
	if err != nil {
		return err
	}
	rewritten := tableForeignKeyPattern.ReplaceAllString(definition, "")
	rewritten = inlineReferencePattern.ReplaceAllString(rewritten, "")
	if strings.Contains(strings.ToUpper(rewritten), "REFERENCES") {
		return fmt.Errorf("unsupported foreign-key clause while rebuilding %s", table)
	}
	opening := strings.IndexByte(rewritten, '(')
	if opening < 0 {
		return fmt.Errorf("invalid table schema for %s", table)
	}

	artifacts, err := explicitSchemaArtifacts(ctx, tx, table)
	if err != nil {
		return err
	}
	columns, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return fmt.Errorf("table %s has no columns", table)
	}

	temporary := "__logical_references_" + table
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteSQLiteIdentifier(temporary)); err != nil {
		return fmt.Errorf("clear temporary table for %s: %w", table, err)
	}
	createSQL := `CREATE TABLE ` + quoteSQLiteIdentifier(temporary) + ` ` + rewritten[opening:]
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create logical-reference table for %s: %w", table, err)
	}
	quotedColumns := make([]string, len(columns))
	for index, column := range columns {
		quotedColumns[index] = quoteSQLiteIdentifier(column)
	}
	columnList := strings.Join(quotedColumns, ",")
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+quoteSQLiteIdentifier(temporary)+` (`+columnList+`) SELECT `+columnList+` FROM `+quoteSQLiteIdentifier(table)); err != nil {
		return fmt.Errorf("copy %s during logical-reference migration: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteSQLiteIdentifier(table)); err != nil {
		return fmt.Errorf("replace %s during logical-reference migration: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+quoteSQLiteIdentifier(temporary)+` RENAME TO `+quoteSQLiteIdentifier(table)); err != nil {
		return fmt.Errorf("rename logical-reference table %s: %w", table, err)
	}
	if hasSequence {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name=?`, table); err != nil {
			return fmt.Errorf("clear sequence for %s: %w", table, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)`, table, sequence); err != nil {
			return fmt.Errorf("restore sequence for %s: %w", table, err)
		}
	}
	for _, artifact := range artifacts {
		if _, err := tx.ExecContext(ctx, artifact.sql); err != nil {
			return fmt.Errorf("recreate %s for %s: %w", artifact.name, table, err)
		}
	}
	return nil
}

func sqliteSequence(ctx context.Context, tx *sql.Tx, table string) (int64, bool, error) {
	var sequenceTable int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sqlite_sequence'`).Scan(&sequenceTable); err != nil {
		return 0, false, err
	}
	if sequenceTable == 0 {
		return 0, false, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name=?`, table).Scan(&sequence); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return sequence, true, nil
}

func explicitSchemaArtifacts(ctx context.Context, tx *sql.Tx, table string) ([]schemaArtifact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name,sql FROM sqlite_master
		WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL
		ORDER BY type,name`, table)
	if err != nil {
		return nil, err
	}
	artifacts := make([]schemaArtifact, 0)
	for rows.Next() {
		var artifact schemaArtifact
		if err := rows.Scan(&artifact.name, &artifact.sql); err != nil {
			_ = rows.Close()
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return columns, nil
}

type foreignKeyInspector interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func hardForeignKeyTables(ctx context.Context, queryer foreignKeyInspector) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	result := make([]string, 0)
	for _, table := range tables {
		foreignKeys, err := queryer.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteSQLiteIdentifier(table)+`)`)
		if err != nil {
			return nil, err
		}
		if foreignKeys.Next() {
			result = append(result, table)
		}
		if err := foreignKeys.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
