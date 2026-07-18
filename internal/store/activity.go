package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ActivityRun       = "run"
	ActivityOperation = "operation"
)

var (
	ErrInvalidActivityCursor = errors.New("invalid activity cursor")
	ErrInvalidActivityFilter = errors.New("invalid activity filter")
)

type ActivityFilter struct {
	RecordType string     `json:"recordType,omitempty"`
	ObjectID   string     `json:"objectId,omitempty"`
	Engine     string     `json:"engine,omitempty"`
	Status     string     `json:"status,omitempty"`
	Trigger    string     `json:"trigger,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	From       *time.Time `json:"from,omitempty"`
	To         *time.Time `json:"to,omitempty"`
	Limit      int        `json:"limit"`
	Page       int        `json:"page,omitempty"`
	Cursor     string     `json:"-"`
}

type ActivityItem struct {
	RecordType     string      `json:"recordType"`
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Engine         string      `json:"engine,omitempty"`
	Status         string      `json:"status"`
	Trigger        string      `json:"trigger,omitempty"`
	ObjectType     string      `json:"objectType"`
	ObjectID       string      `json:"objectId"`
	ObjectName     string      `json:"objectName"`
	TaskID         string      `json:"taskId,omitempty"`
	TaskName       string      `json:"taskName,omitempty"`
	RepositoryID   string      `json:"repositoryId,omitempty"`
	RepositoryName string      `json:"repositoryName,omitempty"`
	PlanID         string      `json:"planId,omitempty"`
	PlanName       string      `json:"planName,omitempty"`
	OccurredAt     time.Time   `json:"occurredAt"`
	StartedAt      *time.Time  `json:"startedAt,omitempty"`
	FinishedAt     *time.Time  `json:"finishedAt,omitempty"`
	AttemptCount   int         `json:"attemptCount"`
	ErrorSummary   string      `json:"errorSummary,omitempty"`
	Metrics        *RunMetrics `json:"metrics,omitempty"`
}

type ActivityPage struct {
	Items       []ActivityItem `json:"items"`
	NextCursor  string         `json:"nextCursor,omitempty"`
	Truncated   bool           `json:"truncated"`
	Page        int            `json:"page,omitempty"`
	PageSize    int            `json:"pageSize"`
	Total       int            `json:"total"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Filter      ActivityFilter `json:"filter"`
}

type activityCursor struct {
	Version     int    `json:"v"`
	Fingerprint string `json:"f"`
	OccurredAt  string `json:"at"`
	RecordType  string `json:"type"`
	ID          string `json:"id"`
}

func (s *Store) ListActivity(ctx context.Context, input ActivityFilter) (ActivityPage, error) {
	filter, err := normalizeActivityFilter(input)
	if err != nil {
		return ActivityPage{}, err
	}
	fingerprint := activityFilterFingerprint(filter)
	var cursor *activityCursor
	if filter.Cursor != "" {
		decoded, err := decodeActivityCursor(filter.Cursor, fingerprint)
		if err != nil {
			return ActivityPage{}, err
		}
		cursor = &decoded
	}

	var total int
	if filter.Page > 0 {
		totalQueries, totalArgs := activityQueries(filter, nil)
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+strings.Join(totalQueries, " UNION ALL ")+`)`, totalArgs...).Scan(&total); err != nil {
			return ActivityPage{}, fmt.Errorf("count activity: %w", err)
		}
	}

	queries, args := activityQueries(filter, cursor)
	query := strings.Join(queries, " UNION ALL ") + ` ORDER BY occurred_at DESC,record_type DESC,id DESC LIMIT ?`
	queryLimit := filter.Limit + 1
	if filter.Page > 0 {
		query += ` OFFSET ?`
		queryLimit = filter.Limit
	}
	args = append(args, queryLimit)
	if filter.Page > 0 {
		args = append(args, (filter.Page-1)*filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ActivityPage{}, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()
	items := make([]ActivityItem, 0, queryLimit)
	for rows.Next() {
		item, err := scanActivity(rows)
		if err != nil {
			return ActivityPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ActivityPage{}, err
	}
	page := ActivityPage{Items: items, Page: filter.Page, PageSize: filter.Limit, Total: total, GeneratedAt: time.Now().UTC(), Filter: filter}
	page.Filter.Cursor = ""
	if filter.Page > 0 {
		page.Truncated = filter.Page*filter.Limit < total
	} else if len(page.Items) > filter.Limit {
		page.Items = page.Items[:filter.Limit]
		page.Truncated = true
	}
	if page.Truncated && len(page.Items) > 0 {
		page.NextCursor, err = encodeActivityCursor(fingerprint, page.Items[len(page.Items)-1])
		if err != nil {
			return ActivityPage{}, err
		}
	}
	return page, nil
}

func activityQueries(filter ActivityFilter, cursor *activityCursor) ([]string, []any) {
	queries := make([]string, 0, 2)
	args := make([]any, 0, 32)
	if filter.RecordType == "" || filter.RecordType == ActivityRun {
		query, queryArgs := runActivityQuery(filter, cursor)
		queries = append(queries, query)
		args = append(args, queryArgs...)
	}
	if filter.RecordType == "" || filter.RecordType == ActivityOperation {
		query, queryArgs := operationActivityQuery(filter, cursor)
		queries = append(queries, query)
		args = append(args, queryArgs...)
	}
	return queries, args
}

func runActivityQuery(filter ActivityFilter, cursor *activityCursor) (string, []any) {
	where := make([]string, 0, 10)
	args := make([]any, 0, 20)
	if filter.ObjectID != "" {
		where = append(where, `(r.id=? OR r.task_id=? OR COALESCE(r.plan_id,'')=? OR COALESCE(t.repository_id,'')=?)`)
		args = append(args, filter.ObjectID, filter.ObjectID, filter.ObjectID, filter.ObjectID)
	}
	if filter.Engine != "" {
		where, args = append(where, `COALESCE(t.engine,'')=?`), append(args, filter.Engine)
	}
	if filter.Status != "" {
		where, args = append(where, `r.status=?`), append(args, filter.Status)
	}
	if filter.Trigger != "" {
		where, args = append(where, `r.trigger=?`), append(args, filter.Trigger)
	}
	if filter.Kind != "" {
		where, args = append(where, `'backup'=?`), append(args, filter.Kind)
	}
	if filter.From != nil {
		where, args = append(where, `r.started_at>=?`), append(args, formatTime(*filter.From))
	}
	if filter.To != nil {
		where, args = append(where, `r.started_at<=?`), append(args, formatTime(*filter.To))
	}
	if cursor != nil {
		where = append(where, `(r.started_at,'run',r.id)<(?,?,?)`)
		args = append(args, cursor.OccurredAt, cursor.RecordType, cursor.ID)
	}
	return `SELECT
		'run' AS record_type,r.id AS id,'backup' AS kind,COALESCE(t.engine,'') AS engine,r.status AS status,r.trigger AS trigger,
		'task' AS object_type,r.task_id AS object_id,COALESCE(t.name,r.task_id) AS object_name,
		r.task_id AS task_id,COALESCE(t.name,'') AS task_name,COALESCE(t.repository_id,'') AS repository_id,COALESCE(repo.name,'') AS repository_name,
		COALESCE(r.plan_id,'') AS plan_id,COALESCE(p.name,'') AS plan_name,r.started_at AS occurred_at,r.started_at AS started_at,r.finished_at AS finished_at,r.attempt_count AS attempt_count,
		substr(CAST(COALESCE(json_extract(r.summary_json,'$.error'),'') AS TEXT),1,512) AS error_summary,
		r.duration_ms,r.files_processed,r.files_changed,r.bytes_processed,r.bytes_changed
	FROM runs r
	LEFT JOIN tasks t ON t.id=r.task_id
	LEFT JOIN repositories repo ON repo.id=t.repository_id
	LEFT JOIN plans p ON p.id=r.plan_id` + activityWhere(where), args
}

func operationActivityQuery(filter ActivityFilter, cursor *activityCursor) (string, []any) {
	where := make([]string, 0, 10)
	args := make([]any, 0, 20)
	if filter.ObjectID != "" {
		where = append(where, `(o.id=? OR o.task_id=? OR o.repository_id=? OR COALESCE(t.repository_id,'')=?)`)
		args = append(args, filter.ObjectID, filter.ObjectID, filter.ObjectID, filter.ObjectID)
	}
	engine := `COALESCE(NULLIF(t.engine,''),NULLIF(direct_repo.engine,''),NULLIF(task_repo.engine,''),'')`
	if filter.Engine != "" {
		where, args = append(where, engine+`=?`), append(args, filter.Engine)
	}
	if filter.Status != "" {
		where, args = append(where, `o.status=?`), append(args, filter.Status)
	}
	if filter.Trigger != "" {
		where = append(where, `0=1`)
	}
	if filter.Kind != "" {
		where, args = append(where, `o.kind=?`), append(args, filter.Kind)
	}
	if filter.From != nil {
		where, args = append(where, `o.created_at>=?`), append(args, formatTime(*filter.From))
	}
	if filter.To != nil {
		where, args = append(where, `o.created_at<=?`), append(args, formatTime(*filter.To))
	}
	if cursor != nil {
		where = append(where, `(o.created_at,'operation',o.id)<(?,?,?)`)
		args = append(args, cursor.OccurredAt, cursor.RecordType, cursor.ID)
	}
	return `SELECT
		'operation' AS record_type,o.id AS id,o.kind AS kind,` + engine + ` AS engine,o.status AS status,'' AS trigger,
		CASE WHEN o.task_id<>'' THEN 'task' WHEN o.repository_id<>'' OR COALESCE(t.repository_id,'')<>'' THEN 'repository' ELSE 'operation' END AS object_type,
		CASE WHEN o.task_id<>'' THEN o.task_id WHEN o.repository_id<>'' THEN o.repository_id WHEN COALESCE(t.repository_id,'')<>'' THEN t.repository_id ELSE o.id END AS object_id,
		CASE WHEN o.task_id<>'' THEN COALESCE(t.name,o.task_id) WHEN o.repository_id<>'' THEN COALESCE(direct_repo.name,o.repository_id) WHEN COALESCE(t.repository_id,'')<>'' THEN COALESCE(task_repo.name,t.repository_id) ELSE o.kind END AS object_name,
		o.task_id AS task_id,COALESCE(t.name,'') AS task_name,COALESCE(NULLIF(o.repository_id,''),NULLIF(t.repository_id,''),'') AS repository_id,
		COALESCE(NULLIF(direct_repo.name,''),NULLIF(task_repo.name,''),'') AS repository_name,'' AS plan_id,'' AS plan_name,
		o.created_at AS occurred_at,o.started_at AS started_at,o.finished_at AS finished_at,o.attempt_count AS attempt_count,substr(o.error_summary,1,512) AS error_summary,
		NULL AS duration_ms,NULL AS files_processed,NULL AS files_changed,NULL AS bytes_processed,NULL AS bytes_changed
	FROM operations o
	LEFT JOIN tasks t ON t.id=o.task_id
	LEFT JOIN repositories direct_repo ON direct_repo.id=o.repository_id
	LEFT JOIN repositories task_repo ON task_repo.id=t.repository_id` + activityWhere(where), args
}

func activityWhere(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

type activityScanner interface {
	Scan(...any) error
}

func scanActivity(scanner activityScanner) (ActivityItem, error) {
	var item ActivityItem
	var occurred string
	var started, finished sql.NullString
	var duration, filesProcessed, filesChanged, bytesProcessed, bytesChanged sql.NullInt64
	if err := scanner.Scan(
		&item.RecordType, &item.ID, &item.Kind, &item.Engine, &item.Status, &item.Trigger,
		&item.ObjectType, &item.ObjectID, &item.ObjectName, &item.TaskID, &item.TaskName,
		&item.RepositoryID, &item.RepositoryName, &item.PlanID, &item.PlanName,
		&occurred, &started, &finished, &item.AttemptCount, &item.ErrorSummary,
		&duration, &filesProcessed, &filesChanged, &bytesProcessed, &bytesChanged,
	); err != nil {
		return ActivityItem{}, err
	}
	parsed, err := parseTime(occurred)
	if err != nil {
		return ActivityItem{}, err
	}
	item.OccurredAt = parsed
	if started.Valid {
		value, err := parseTime(started.String)
		if err != nil {
			return ActivityItem{}, err
		}
		item.StartedAt = &value
	}
	if finished.Valid {
		value, err := parseTime(finished.String)
		if err != nil {
			return ActivityItem{}, err
		}
		item.FinishedAt = &value
	}
	metrics := RunMetrics{DurationMilliseconds: nullMetric(duration), FilesProcessed: nullMetric(filesProcessed), FilesChanged: nullMetric(filesChanged), BytesProcessed: nullMetric(bytesProcessed), BytesChanged: nullMetric(bytesChanged)}
	if metrics.DurationMilliseconds != nil || metrics.FilesProcessed != nil || metrics.FilesChanged != nil || metrics.BytesProcessed != nil || metrics.BytesChanged != nil {
		item.Metrics = &metrics
	}
	return item, nil
}

func normalizeActivityFilter(input ActivityFilter) (ActivityFilter, error) {
	filter := input
	filter.RecordType = strings.TrimSpace(filter.RecordType)
	filter.ObjectID = strings.TrimSpace(filter.ObjectID)
	filter.Engine = strings.TrimSpace(filter.Engine)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Trigger = strings.TrimSpace(filter.Trigger)
	filter.Kind = strings.TrimSpace(filter.Kind)
	if filter.RecordType != "" && filter.RecordType != ActivityRun && filter.RecordType != ActivityOperation {
		return ActivityFilter{}, fmt.Errorf("%w: invalid record type", ErrInvalidActivityFilter)
	}
	for name, value := range map[string]string{
		"object": filter.ObjectID, "engine": filter.Engine, "status": filter.Status,
		"trigger": filter.Trigger, "kind": filter.Kind,
	} {
		if len(value) > 256 || strings.ContainsRune(value, '\x00') {
			return ActivityFilter{}, fmt.Errorf("%w: invalid %s", ErrInvalidActivityFilter, name)
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return ActivityFilter{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrInvalidActivityFilter)
	}
	if filter.Page < 0 || filter.Page > 1_000_000 {
		return ActivityFilter{}, fmt.Errorf("%w: page must be between 1 and 1000000", ErrInvalidActivityFilter)
	}
	if filter.Page > 0 && filter.Cursor != "" {
		return ActivityFilter{}, fmt.Errorf("%w: page and cursor are mutually exclusive", ErrInvalidActivityFilter)
	}
	if len(filter.Cursor) > 2048 {
		return ActivityFilter{}, ErrInvalidActivityCursor
	}
	if filter.From != nil {
		value := filter.From.UTC()
		filter.From = &value
	}
	if filter.To != nil {
		value := filter.To.UTC()
		filter.To = &value
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return ActivityFilter{}, fmt.Errorf("%w: start time must not be after end time", ErrInvalidActivityFilter)
	}
	return filter, nil
}

func activityFilterFingerprint(filter ActivityFilter) string {
	type fingerprintFilter struct {
		RecordType string `json:"recordType,omitempty"`
		ObjectID   string `json:"objectId,omitempty"`
		Engine     string `json:"engine,omitempty"`
		Status     string `json:"status,omitempty"`
		Trigger    string `json:"trigger,omitempty"`
		Kind       string `json:"kind,omitempty"`
		From       string `json:"from,omitempty"`
		To         string `json:"to,omitempty"`
		Limit      int    `json:"limit"`
	}
	value := fingerprintFilter{RecordType: filter.RecordType, ObjectID: filter.ObjectID, Engine: filter.Engine, Status: filter.Status, Trigger: filter.Trigger, Kind: filter.Kind, Limit: filter.Limit}
	if filter.From != nil {
		value.From = formatTime(*filter.From)
	}
	if filter.To != nil {
		value.To = formatTime(*filter.To)
	}
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func encodeActivityCursor(fingerprint string, item ActivityItem) (string, error) {
	encoded, err := json.Marshal(activityCursor{Version: 1, Fingerprint: fingerprint, OccurredAt: formatTime(item.OccurredAt), RecordType: item.RecordType, ID: item.ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeActivityCursor(raw, fingerprint string) (activityCursor, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(encoded) > 1024 {
		return activityCursor{}, ErrInvalidActivityCursor
	}
	var cursor activityCursor
	if err := json.Unmarshal(encoded, &cursor); err != nil || cursor.Version != 1 || cursor.Fingerprint != fingerprint || cursor.ID == "" || (cursor.RecordType != ActivityRun && cursor.RecordType != ActivityOperation) {
		return activityCursor{}, ErrInvalidActivityCursor
	}
	at, err := parseTime(cursor.OccurredAt)
	if err != nil || at.After(time.Now().UTC().Add(5*time.Minute)) {
		return activityCursor{}, ErrInvalidActivityCursor
	}
	cursor.OccurredAt = formatTime(at)
	return cursor, nil
}
