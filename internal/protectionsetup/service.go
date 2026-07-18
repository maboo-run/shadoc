package protectionsetup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

const maximumDraftItems = 100

type Storage interface {
	CreateProtectionDraft(context.Context, Draft) error
	ProtectionDraft(context.Context, string) (Draft, error)
	ListProtectionDrafts(context.Context) ([]Draft, error)
	UpdateProtectionDraftStatus(context.Context, string, string, time.Time) error
	UpdateProtectionDraftItem(context.Context, DraftItem) error
	CreateProtectionTemplate(context.Context, Template) error
	ProtectionTemplate(context.Context, string) (Template, error)
	ListProtectionTemplates(context.Context) ([]Template, error)
	ListRepositories(context.Context) ([]domain.Repository, error)
	CreateRepository(context.Context, domain.Repository, string) error
	ListTasks(context.Context) ([]domain.Task, error)
	CreateTask(context.Context, domain.Task) error
	ListPlans(context.Context) ([]domain.Plan, error)
	CreatePlan(context.Context, domain.Plan) error
	UpdatePlan(context.Context, domain.Plan) error
	SaveMaintenancePolicy(context.Context, domain.MaintenancePolicy) error
	ListDatabaseConnections(context.Context) ([]domain.DatabaseConnection, error)
}

type Secrets interface {
	Put(context.Context, string, []byte) (string, error)
	Delete(context.Context, string) error
}

type RepositoryInitializer interface {
	Initialize(context.Context, string) error
}

type Service struct {
	storage     Storage
	secrets     Secrets
	initializer RepositoryInitializer
	now         func() time.Time
	newID       func(string) string
}

func New(storage Storage, secrets Secrets, initializer RepositoryInitializer, now func() time.Time, idGenerator func(string) string) *Service {
	if now == nil {
		now = time.Now
	}
	if idGenerator == nil {
		idGenerator = randomID
	}
	return &Service{storage: storage, secrets: secrets, initializer: initializer, now: now, newID: idGenerator}
}

func (s *Service) CreateTemplate(ctx context.Context, input TemplateInput) (Template, error) {
	now := s.now().UTC()
	item := Template{
		ID: s.newID("protection-template"), Name: strings.TrimSpace(input.Name), Retention: input.Retention, Resources: input.Resources, Health: input.Health,
		Schedule: input.Schedule, Timezone: input.Timezone, MaxParallel: input.MaxParallel, CatchUpWindowMinutes: input.CatchUpWindowMinutes,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := item.Validate(); err != nil {
		return Template{}, err
	}
	if err := s.storage.CreateProtectionTemplate(ctx, item); err != nil {
		return Template{}, err
	}
	return item, nil
}

func (s *Service) ListTemplates(ctx context.Context) ([]Template, error) {
	return s.storage.ListProtectionTemplates(ctx)
}

func (s *Service) CreateDraft(ctx context.Context, input CreateDraftInput) (Draft, error) {
	if s.storage == nil || s.secrets == nil || s.initializer == nil {
		return Draft{}, errors.New("protection setup service is not configured")
	}
	if input.TemplateID != "" {
		template, err := s.storage.ProtectionTemplate(ctx, input.TemplateID)
		if err != nil {
			return Draft{}, err
		}
		input.Retention, input.Resources, input.Health = template.Retention, template.Resources, template.Health
		input.Schedule, input.Timezone = template.Schedule, template.Timezone
		input.MaxParallel, input.CatchUpWindowMinutes = template.MaxParallel, template.CatchUpWindowMinutes
	}
	now := s.now().UTC()
	draft := Draft{
		ID: s.newID("protection-draft"), Name: strings.TrimSpace(input.Name), TemplateID: input.TemplateID, ExecutionTarget: input.ExecutionTarget.Normalized(),
		Retention: input.Retention, Resources: input.Resources, Health: input.Health, Schedule: input.Schedule, Timezone: input.Timezone,
		MaxParallel: input.MaxParallel, CatchUpWindowMinutes: input.CatchUpWindowMinutes, NotificationMode: input.NotificationMode,
		PlanID: s.newID("plan"), Status: DraftPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.validateDraftInput(ctx, draft, input.Items); err != nil {
		return Draft{}, err
	}
	staged := make([]string, 0, len(input.Items))
	for position, inputItem := range input.Items {
		secretID, err := s.secrets.Put(ctx, "repository-password", []byte(inputItem.Password))
		if err != nil {
			cleanupCtx := context.WithoutCancel(ctx)
			for _, id := range staged {
				_ = s.secrets.Delete(cleanupCtx, id)
			}
			return Draft{}, errors.New("stage repository password")
		}
		staged = append(staged, secretID)
		item := DraftItem{
			ID: s.newID("protection-item"), DraftID: draft.ID, Position: position, TaskName: strings.TrimSpace(inputItem.TaskName),
			Directory: cloneDirectory(inputItem.Directory), Database: cloneDatabase(inputItem.Database),
			RepositoryID: s.newID("repo"), RepositoryName: strings.TrimSpace(inputItem.RepositoryName), RepositoryKind: inputItem.RepositoryKind,
			RemoteHostID: strings.TrimSpace(inputItem.RemoteHostID), RepositoryPath: strings.TrimSpace(inputItem.RepositoryPath),
			TaskID: s.newID("task"), Status: ItemPending, PasswordSecretID: secretID, UpdatedAt: now,
		}
		draft.Items = append(draft.Items, item)
	}
	if err := s.storage.CreateProtectionDraft(ctx, draft); err != nil {
		cleanupCtx := context.WithoutCancel(ctx)
		for _, id := range staged {
			_ = s.secrets.Delete(cleanupCtx, id)
		}
		return Draft{}, err
	}
	return publicDraft(draft), nil
}

func (s *Service) Draft(ctx context.Context, id string) (Draft, error) {
	item, err := s.storage.ProtectionDraft(ctx, id)
	return publicDraft(item), err
}

func (s *Service) ListDrafts(ctx context.Context) ([]Draft, error) {
	items, err := s.storage.ListProtectionDrafts(ctx)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index] = publicDraft(items[index])
	}
	return items, nil
}

func (s *Service) Apply(ctx context.Context, id string, reporter StageReporter) (Draft, error) {
	if s.initializer == nil {
		return Draft{}, errors.New("repository initializer is unavailable")
	}
	draft, err := s.storage.ProtectionDraft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if draft.Status == DraftCancelled {
		return publicDraft(draft), errors.New("cancelled protection draft cannot be applied")
	}
	if draft.Status == DraftReady {
		return publicDraft(draft), nil
	}
	now := s.now().UTC()
	if err := s.storage.UpdateProtectionDraftStatus(ctx, id, string(DraftApplying), now); err != nil {
		return Draft{}, err
	}
	failed := 0
	for index := range draft.Items {
		item := draft.Items[index]
		if item.Status == ItemReady {
			continue
		}
		if reporter != nil {
			reporter("protection_item", map[string]any{"itemId": item.ID, "position": item.Position, "status": "running"})
		}
		if stage, err := s.applyItem(ctx, draft, &item); err != nil {
			failed++
			item.Status, item.Error, item.UpdatedAt = ItemFailed, safeItemError(stage), s.now().UTC()
			_ = s.storage.UpdateProtectionDraftItem(context.WithoutCancel(ctx), item)
			if reporter != nil {
				reporter("protection_item", map[string]any{"itemId": item.ID, "position": item.Position, "status": "failed", "stage": stage})
			}
			draft.Items[index] = item
			continue
		}
		item.Status, item.Error, item.UpdatedAt = ItemReady, "", s.now().UTC()
		if err := s.storage.UpdateProtectionDraftItem(context.WithoutCancel(ctx), item); err != nil {
			failed++
			item.Status, item.Error = ItemFailed, safeItemError("record_result")
		}
		if reporter != nil {
			reporter("protection_item", map[string]any{"itemId": item.ID, "position": item.Position, "status": string(item.Status)})
		}
		draft.Items[index] = item
	}
	if err := s.upsertPlan(ctx, draft); err != nil {
		failed++
	}
	status := DraftReady
	if failed > 0 {
		status = DraftPartial
	}
	if err := s.storage.UpdateProtectionDraftStatus(context.WithoutCancel(ctx), id, string(status), s.now().UTC()); err != nil {
		return Draft{}, err
	}
	result, loadErr := s.storage.ProtectionDraft(context.WithoutCancel(ctx), id)
	if loadErr != nil {
		return Draft{}, loadErr
	}
	if failed > 0 {
		return publicDraft(result), errors.New("one or more protection items failed")
	}
	return publicDraft(result), nil
}

func (s *Service) applyItem(ctx context.Context, draft Draft, item *DraftItem) (string, error) {
	repositories, err := s.storage.ListRepositories(ctx)
	if err != nil {
		return "inspect_repository", err
	}
	repository, exists := repositoryByID(repositories, item.RepositoryID)
	if !exists {
		repository = domain.Repository{
			ID: item.RepositoryID, Name: item.RepositoryName, Engine: domain.ResticEngine, Kind: item.RepositoryKind,
			RemoteHostID: item.RemoteHostID, Path: item.RepositoryPath, Status: "uninitialized", CreatedAt: draft.CreatedAt, UpdatedAt: s.now().UTC(),
		}
		if err := s.storage.CreateRepository(ctx, repository, item.PasswordSecretID); err != nil {
			return "create_repository", err
		}
	}
	if repository.Status != "ready" {
		if err := s.initializer.Initialize(ctx, repository.ID); err != nil {
			return "initialize_repository", err
		}
	}
	tasks, err := s.storage.ListTasks(ctx)
	if err != nil {
		return "inspect_task", err
	}
	if !taskExists(tasks, item.TaskID) {
		task := domain.Task{
			ID: item.TaskID, Name: item.TaskName, Engine: domain.ResticEngine, Kind: itemKind(*item), ExecutionTarget: draft.ExecutionTarget,
			RepositoryID: item.RepositoryID, Directory: cloneDirectory(item.Directory), Database: cloneDatabase(item.Database), Retention: draft.Retention,
			Resources: draft.Resources, Health: draft.Health, Enabled: false, CreatedAt: draft.CreatedAt, UpdatedAt: s.now().UTC(),
		}
		if err := task.Validate(); err != nil {
			return "validate_task", err
		}
		if err := s.storage.CreateTask(ctx, task); err != nil {
			return "create_task", err
		}
	}
	maintenance := domain.MaintenancePolicy{
		RepositoryID: item.RepositoryID, Schedule: domain.Schedule{Kind: domain.WeeklySchedule, DayOfWeek: time.Sunday, TimeOfDay: "03:00"},
		Timezone: draft.Timezone, Retention: draft.Retention, Enabled: false, CatchUpWindowMinutes: draft.CatchUpWindowMinutes,
		ScheduleAnchorAt: draft.CreatedAt, UpdatedAt: s.now().UTC(),
	}
	if err := maintenance.Validate(); err != nil {
		return "validate_maintenance", err
	}
	if err := s.storage.SaveMaintenancePolicy(ctx, maintenance); err != nil {
		return "create_maintenance", err
	}
	return "", nil
}

func (s *Service) upsertPlan(ctx context.Context, draft Draft) error {
	taskIDs := make([]string, 0, len(draft.Items))
	for _, item := range draft.Items {
		if item.Status == ItemReady {
			taskIDs = append(taskIDs, item.TaskID)
		}
	}
	if len(taskIDs) == 0 {
		return nil
	}
	plan := domain.Plan{
		ID: draft.PlanID, Name: draft.Name, Schedule: draft.Schedule, Timezone: draft.Timezone, MaxParallel: draft.MaxParallel,
		TaskIDs: taskIDs, Enabled: false, CatchUpWindowMinutes: draft.CatchUpWindowMinutes,
		ScheduleAnchorAt: draft.CreatedAt, CreatedAt: draft.CreatedAt, UpdatedAt: s.now().UTC(),
	}
	plans, err := s.storage.ListPlans(ctx)
	if err != nil {
		return err
	}
	for _, existing := range plans {
		if existing.ID == plan.ID {
			plan.CreatedAt, plan.ScheduleAnchorAt = existing.CreatedAt, existing.ScheduleAnchorAt
			return s.storage.UpdatePlan(ctx, plan)
		}
	}
	return s.storage.CreatePlan(ctx, plan)
}

func (s *Service) Cancel(ctx context.Context, id string) (Draft, error) {
	draft, err := s.storage.ProtectionDraft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if draft.Status == DraftReady {
		return publicDraft(draft), errors.New("completed protection draft cannot be cancelled")
	}
	if draft.Status == DraftApplying {
		return publicDraft(draft), errors.New("applying protection draft cannot be cancelled")
	}
	cleanupCtx := context.WithoutCancel(ctx)
	repositories, err := s.storage.ListRepositories(cleanupCtx)
	if err != nil {
		return Draft{}, err
	}
	for index := range draft.Items {
		item := draft.Items[index]
		if _, exists := repositoryByID(repositories, item.RepositoryID); exists {
			item.Status = ItemRetained
		} else {
			if item.PasswordSecretID != "" {
				if err := s.secrets.Delete(cleanupCtx, item.PasswordSecretID); err != nil {
					return Draft{}, err
				}
			}
			item.PasswordSecretID, item.Status = "", ItemCancelled
		}
		item.Error, item.UpdatedAt = "", s.now().UTC()
		if err := s.storage.UpdateProtectionDraftItem(cleanupCtx, item); err != nil {
			return Draft{}, err
		}
	}
	if err := s.storage.UpdateProtectionDraftStatus(cleanupCtx, id, string(DraftCancelled), s.now().UTC()); err != nil {
		return Draft{}, err
	}
	result, err := s.storage.ProtectionDraft(cleanupCtx, id)
	return publicDraft(result), err
}

func (s *Service) validateDraftInput(ctx context.Context, draft Draft, items []CreateDraftItem) error {
	if strings.TrimSpace(draft.Name) == "" || len(draft.Name) > 200 {
		return errors.New("protection draft name is required")
	}
	if err := draft.ExecutionTarget.Validate(); err != nil {
		return err
	}
	if err := draft.Retention.Validate(); err != nil {
		return err
	}
	if err := draft.Resources.Validate(); err != nil {
		return err
	}
	if err := draft.Health.Validate(); err != nil {
		return err
	}
	if draft.NotificationMode != NotificationConfigured && draft.NotificationMode != NotificationNone {
		return errors.New("notification selection is required")
	}
	if len(items) == 0 || len(items) > maximumDraftItems {
		return errors.New("protection draft requires between 1 and 100 items")
	}
	probeTaskIDs := make([]string, len(items))
	for index := range probeTaskIDs {
		probeTaskIDs[index] = fmt.Sprintf("task-%d", index)
	}
	if err := (domain.Plan{Name: draft.Name, Schedule: draft.Schedule, Timezone: draft.Timezone, MaxParallel: draft.MaxParallel, TaskIDs: probeTaskIDs, CatchUpWindowMinutes: draft.CatchUpWindowMinutes}).Validate(); err != nil {
		return err
	}
	connections, err := s.storage.ListDatabaseConnections(ctx)
	if err != nil {
		return err
	}
	existingRepositories, err := s.storage.ListRepositories(ctx)
	if err != nil {
		return err
	}
	existingTasks, err := s.storage.ListTasks(ctx)
	if err != nil {
		return err
	}
	paths, repositoryNames, taskNames, sources := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	now := s.now().UTC()
	for _, item := range items {
		if strings.TrimSpace(item.TaskName) == "" || strings.TrimSpace(item.RepositoryName) == "" || len(item.Password) < 12 || !item.PasswordConfirmed {
			return errors.New("each mapping requires names and a confirmed repository password")
		}
		if (item.Directory == nil) == (item.Database == nil) {
			return errors.New("each mapping requires exactly one source")
		}
		probeTask := domain.Task{
			Name: item.TaskName, Engine: domain.ResticEngine, ExecutionTarget: draft.ExecutionTarget, RepositoryID: "draft-validation",
			Directory: cloneDirectory(item.Directory), Database: cloneDatabase(item.Database), Retention: draft.Retention, Resources: draft.Resources, Health: draft.Health,
		}
		if item.Database != nil {
			probeTask.Kind = domain.DatabaseTask
		} else {
			probeTask.Kind = domain.DirectoryTask
		}
		if err := probeTask.Validate(); err != nil {
			return err
		}
		if item.Database != nil {
			if draft.ExecutionTarget.Kind != execution.Local {
				return errors.New("database protection currently requires local service execution")
			}
			connection, ok := databaseConnectionByID(connections, item.Database.ConnectionID)
			if !ok || connection.Purpose != domain.BackupConnection || connection.Status != "ready" || connection.Preflight.Error != "" || connection.Preflight.CheckedAt.IsZero() || connection.Preflight.CheckedAt.After(now) || now.Sub(connection.Preflight.CheckedAt) > 24*time.Hour {
				return errors.New("database source requires a freshly verified backup connection")
			}
		}
		repository := domain.Repository{Name: item.RepositoryName, Kind: item.RepositoryKind, RemoteHostID: item.RemoteHostID, Path: item.RepositoryPath}
		if err := repository.Validate(); err != nil {
			return err
		}
		if draft.ExecutionTarget.Kind == execution.Agent && repository.EffectiveKind() != domain.SFTPRepository {
			return errors.New("Agent directory protection requires an SFTP repository")
		}
		pathKey := string(repository.EffectiveKind()) + "\x00" + repository.RemoteHostID + "\x00" + repository.Path
		sourceKey := sourceFingerprint(item)
		keys := []struct {
			value string
			set   map[string]struct{}
		}{
			{strings.ToLower(strings.TrimSpace(item.TaskName)), taskNames},
			{strings.ToLower(strings.TrimSpace(item.RepositoryName)), repositoryNames},
			{pathKey, paths},
			{sourceKey, sources},
		}
		for _, key := range keys {
			if _, exists := key.set[key.value]; exists {
				return errors.New("draft mappings must use unique sources, task names, repositories and paths")
			}
			key.set[key.value] = struct{}{}
		}
		for _, existing := range existingRepositories {
			if strings.EqualFold(existing.Name, repository.Name) || (existing.EffectiveKind() == repository.EffectiveKind() && existing.RemoteHostID == repository.RemoteHostID && existing.Path == repository.Path) {
				return errors.New("repository mapping conflicts with an existing repository")
			}
		}
		for _, existing := range existingTasks {
			if strings.EqualFold(existing.Name, strings.TrimSpace(item.TaskName)) {
				return errors.New("task mapping conflicts with an existing task")
			}
		}
	}
	return nil
}

func publicDraft(value Draft) Draft {
	value.Items = append([]DraftItem(nil), value.Items...)
	sort.Slice(value.Items, func(i, j int) bool { return value.Items[i].Position < value.Items[j].Position })
	for index := range value.Items {
		value.Items[index].HasPassword = value.Items[index].PasswordSecretID != ""
		value.Items[index].PasswordSecretID = ""
	}
	return value
}

func repositoryByID(items []domain.Repository, id string) (domain.Repository, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return domain.Repository{}, false
}

func taskExists(items []domain.Task, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func databaseConnectionByID(items []domain.DatabaseConnection, id string) (domain.DatabaseConnection, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return domain.DatabaseConnection{}, false
}

func itemKind(item DraftItem) domain.TaskKind {
	if item.Database != nil {
		return domain.DatabaseTask
	}
	return domain.DirectoryTask
}

func sourceFingerprint(item CreateDraftItem) string {
	if item.Database != nil {
		return "database\x00" + item.Database.ConnectionID + "\x00" + item.Database.Database
	}
	return "directory\x00" + item.Directory.Path
}

func cloneDirectory(value *domain.DirectorySource) *domain.DirectorySource {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Exclusions = append([]string(nil), value.Exclusions...)
	return &copy
}

func cloneDatabase(value *domain.DatabaseSource) *domain.DatabaseSource {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func safeItemError(stage string) string {
	switch stage {
	case "create_repository":
		return "无法创建独立仓库"
	case "initialize_repository":
		return "独立仓库初始化失败"
	case "create_task", "validate_task":
		return "无法创建停用任务草稿"
	case "create_maintenance", "validate_maintenance":
		return "无法创建维护策略草稿"
	case "record_result":
		return "资源已创建，但无法保存向导结果"
	default:
		return "无法完成该保护对象"
	}
}

func randomID(prefix string) string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(value)
}
