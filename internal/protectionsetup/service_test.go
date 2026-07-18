package protectionsetup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

type setupStorage struct {
	drafts       map[string]Draft
	templates    map[string]Template
	repositories map[string]domain.Repository
	tasks        map[string]domain.Task
	plans        map[string]domain.Plan
	maintenance  map[string]domain.MaintenancePolicy
	connections  []domain.DatabaseConnection
	createOrder  []string
}

func newSetupStorage() *setupStorage {
	return &setupStorage{
		drafts: make(map[string]Draft), templates: make(map[string]Template), repositories: make(map[string]domain.Repository),
		tasks: make(map[string]domain.Task), plans: make(map[string]domain.Plan), maintenance: make(map[string]domain.MaintenancePolicy),
	}
}

func cloneDraft(value Draft) Draft {
	value.Items = append([]DraftItem(nil), value.Items...)
	return value
}

func (s *setupStorage) CreateProtectionDraft(_ context.Context, value Draft) error {
	s.drafts[value.ID] = cloneDraft(value)
	return nil
}
func (s *setupStorage) ProtectionDraft(_ context.Context, id string) (Draft, error) {
	value, ok := s.drafts[id]
	if !ok {
		return Draft{}, sql.ErrNoRows
	}
	return cloneDraft(value), nil
}
func (s *setupStorage) ListProtectionDrafts(context.Context) ([]Draft, error) {
	items := make([]Draft, 0, len(s.drafts))
	for _, item := range s.drafts {
		items = append(items, cloneDraft(item))
	}
	return items, nil
}
func (s *setupStorage) UpdateProtectionDraftStatus(_ context.Context, id, status string, updated time.Time) error {
	value := s.drafts[id]
	value.Status, value.UpdatedAt = DraftStatus(status), updated
	s.drafts[id] = value
	return nil
}
func (s *setupStorage) UpdateProtectionDraftItem(_ context.Context, item DraftItem) error {
	value := s.drafts[item.DraftID]
	for index := range value.Items {
		if value.Items[index].ID == item.ID {
			value.Items[index] = item
		}
	}
	value.UpdatedAt = item.UpdatedAt
	s.drafts[item.DraftID] = value
	return nil
}
func (s *setupStorage) CreateProtectionTemplate(_ context.Context, value Template) error {
	s.templates[value.ID] = value
	return nil
}
func (s *setupStorage) ProtectionTemplate(_ context.Context, id string) (Template, error) {
	value, ok := s.templates[id]
	if !ok {
		return Template{}, sql.ErrNoRows
	}
	return value, nil
}
func (s *setupStorage) ListProtectionTemplates(context.Context) ([]Template, error) {
	items := make([]Template, 0, len(s.templates))
	for _, item := range s.templates {
		items = append(items, item)
	}
	return items, nil
}
func (s *setupStorage) ListRepositories(context.Context) ([]domain.Repository, error) {
	items := make([]domain.Repository, 0, len(s.repositories))
	for _, item := range s.repositories {
		items = append(items, item)
	}
	return items, nil
}
func (s *setupStorage) CreateRepository(_ context.Context, value domain.Repository, _ string) error {
	if _, exists := s.repositories[value.ID]; exists {
		return errors.New("duplicate repository")
	}
	s.repositories[value.ID] = value
	s.createOrder = append(s.createOrder, "repository:"+value.ID)
	return nil
}
func (s *setupStorage) ListTasks(context.Context) ([]domain.Task, error) {
	items := make([]domain.Task, 0, len(s.tasks))
	for _, item := range s.tasks {
		items = append(items, item)
	}
	return items, nil
}
func (s *setupStorage) CreateTask(_ context.Context, value domain.Task) error {
	if s.repositories[value.RepositoryID].Status != "ready" {
		return errors.New("repository not ready")
	}
	s.tasks[value.ID] = value
	s.createOrder = append(s.createOrder, "task:"+value.ID)
	return nil
}
func (s *setupStorage) ListPlans(context.Context) ([]domain.Plan, error) {
	items := make([]domain.Plan, 0, len(s.plans))
	for _, item := range s.plans {
		items = append(items, item)
	}
	return items, nil
}
func (s *setupStorage) CreatePlan(_ context.Context, value domain.Plan) error {
	s.plans[value.ID] = value
	return nil
}
func (s *setupStorage) UpdatePlan(_ context.Context, value domain.Plan) error {
	s.plans[value.ID] = value
	return nil
}
func (s *setupStorage) SaveMaintenancePolicy(_ context.Context, value domain.MaintenancePolicy) error {
	s.maintenance[value.RepositoryID] = value
	return nil
}
func (s *setupStorage) ListDatabaseConnections(context.Context) ([]domain.DatabaseConnection, error) {
	return append([]domain.DatabaseConnection(nil), s.connections...), nil
}

type setupSecrets struct {
	next    int
	values  map[string]string
	deleted []string
}

type cancellingCleanupSecrets struct {
	*setupSecrets
	cancel context.CancelFunc
}

func (s *cancellingCleanupSecrets) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(s.deleted) == 0 {
		s.cancel()
	}
	return s.setupSecrets.Delete(ctx, id)
}

type cancellingSetupSecrets struct {
	putCount int
	values   map[string]string
	cancel   context.CancelFunc
}

func (s *cancellingSetupSecrets) Put(ctx context.Context, _ string, value []byte) (string, error) {
	s.putCount++
	if s.putCount == 2 {
		s.cancel()
		return "", ctx.Err()
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values["secret-1"] = string(value)
	return "secret-1", nil
}

func (s *cancellingSetupSecrets) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(s.values, id)
	return nil
}

func (s *setupSecrets) Put(_ context.Context, purpose string, value []byte) (string, error) {
	if purpose != "repository-password" {
		return "", errors.New("unexpected purpose")
	}
	s.next++
	id := "secret-" + string(rune('0'+s.next))
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[id] = string(value)
	return id, nil
}
func (s *setupSecrets) Delete(_ context.Context, id string) error {
	delete(s.values, id)
	s.deleted = append(s.deleted, id)
	return nil
}

type setupInitializer struct {
	storage *setupStorage
	fail    map[string]int
	calls   []string
}

func (i *setupInitializer) Initialize(_ context.Context, id string) error {
	i.calls = append(i.calls, id)
	if i.fail[id] > 0 {
		i.fail[id]--
		return errors.New("controlled initialization failure")
	}
	repository := i.storage.repositories[id]
	repository.Status = "ready"
	i.storage.repositories[id] = repository
	return nil
}

func setupService(storage *setupStorage, secrets Secrets, initializer *setupInitializer, now time.Time) *Service {
	sequence := 0
	return New(storage, secrets, initializer, func() time.Time { return now }, func(prefix string) string {
		sequence++
		return prefix + "-" + string(rune('a'+sequence-1))
	})
}

func databaseDraftInput(now time.Time) CreateDraftInput {
	return CreateDraftInput{
		Name: "Production databases", ExecutionTarget: execution.Target{Kind: execution.Local},
		Retention: domain.RetentionPolicy{KeepDaily: 7, KeepMonthly: 6}, Resources: domain.ResourcePolicy{Compression: "auto"},
		Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "Asia/Shanghai", MaxParallel: 2, CatchUpWindowMinutes: 120,
		NotificationMode: NotificationConfigured,
		Items: []CreateDraftItem{
			{TaskName: "Accounts", Database: &domain.DatabaseSource{ConnectionID: "connection", Database: "accounts"}, RepositoryName: "Accounts repository", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/accounts", Password: "accounts-secret", PasswordConfirmed: true},
			{TaskName: "Orders", Database: &domain.DatabaseSource{ConnectionID: "connection", Database: "orders"}, RepositoryName: "Orders repository", RepositoryKind: domain.LocalRepository, RepositoryPath: "/backup/orders", Password: "orders-secret", PasswordConfirmed: true},
		},
	}
}

func TestCreateDraftStagesOnlyRepositorySecretsAndPersistsCompleteMapping(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Name: "mysql", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now.Add(-time.Hour)}}}
	secrets := &setupSecrets{}
	service := setupService(storage, secrets, &setupInitializer{storage: storage}, now)
	draft, err := service.CreateDraft(t.Context(), databaseDraftInput(now))
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if draft.Status != DraftPending || len(draft.Items) != 2 || draft.PlanID == "" {
		t.Fatalf("draft=%+v", draft)
	}
	if draft.Items[0].RepositoryID == draft.Items[1].RepositoryID || draft.Items[0].TaskID == draft.Items[1].TaskID || draft.Items[0].RepositoryPath == draft.Items[1].RepositoryPath {
		t.Fatalf("mapping reused a resource: %+v", draft.Items)
	}
	if !draft.Items[0].HasPassword || draft.Items[0].PasswordSecretID != "" || len(secrets.values) != 2 {
		t.Fatalf("public draft leaked or lost staged secret: %+v values=%v", draft.Items[0], secrets.values)
	}
	persisted := storage.drafts[draft.ID]
	if persisted.Items[0].PasswordSecretID == "" || persisted.Items[0].HasPassword {
		t.Fatalf("internal staged secret reference invalid: %+v", persisted.Items[0])
	}
}

func TestCreateDraftRejectsDatabasePreflightFromTheFuture(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now.Add(time.Minute)}}}
	service := setupService(storage, &setupSecrets{}, &setupInitializer{storage: storage}, now)
	if _, err := service.CreateDraft(t.Context(), databaseDraftInput(now)); err == nil {
		t.Fatal("future database preflight accepted")
	}
}

func TestCreateDraftCleansStagedSecretsAfterRequestCancellation(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
	ctx, cancel := context.WithCancel(t.Context())
	secrets := &cancellingSetupSecrets{cancel: cancel}
	service := setupService(storage, secrets, &setupInitializer{storage: storage}, now)
	if _, err := service.CreateDraft(ctx, databaseDraftInput(now)); err == nil {
		t.Fatal("cancelled secret staging unexpectedly succeeded")
	}
	if len(secrets.values) != 0 {
		t.Fatalf("staged secret leaked after cancellation: %v", secrets.values)
	}
}

func TestCreateDraftRejectsInvalidSourceAndExistingTaskBeforeStagingSecrets(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	for _, configure := range []func(*setupStorage, *CreateDraftInput){
		func(_ *setupStorage, input *CreateDraftInput) {
			input.Items[0].Database = nil
			input.Items[0].Directory = &domain.DirectorySource{Path: "relative/path"}
		},
		func(storage *setupStorage, input *CreateDraftInput) {
			storage.tasks["existing"] = domain.Task{ID: "existing", Name: input.Items[0].TaskName}
		},
	} {
		storage := newSetupStorage()
		storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
		input := databaseDraftInput(now)
		configure(storage, &input)
		secrets := &setupSecrets{}
		service := setupService(storage, secrets, &setupInitializer{storage: storage}, now)
		if _, err := service.CreateDraft(t.Context(), input); err == nil {
			t.Fatal("invalid draft mapping accepted")
		}
		if len(secrets.values) != 0 {
			t.Fatalf("invalid draft staged secrets: %v", secrets.values)
		}
	}
}

func TestApplyCreatesOneRepositoryAndTaskPerDatabaseThenDisabledSharedPlan(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Name: "mysql", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
	secrets := &setupSecrets{}
	initializer := &setupInitializer{storage: storage, fail: make(map[string]int)}
	service := setupService(storage, secrets, initializer, now)
	draft, err := service.CreateDraft(t.Context(), databaseDraftInput(now))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), draft.ID, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Status != DraftReady || len(storage.repositories) != 2 || len(storage.tasks) != 2 || len(storage.maintenance) != 2 || len(storage.plans) != 1 {
		t.Fatalf("result=%+v repositories=%d tasks=%d maintenance=%d plans=%d", result, len(storage.repositories), len(storage.tasks), len(storage.maintenance), len(storage.plans))
	}
	plan := storage.plans[draft.PlanID]
	if plan.Enabled || len(plan.TaskIDs) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	for _, task := range storage.tasks {
		if task.Enabled || task.Database == nil || task.RepositoryID == "" {
			t.Fatalf("unsafe generated task=%+v", task)
		}
		if storage.maintenance[task.RepositoryID].Enabled {
			t.Fatalf("maintenance must await a dry-run: %+v", storage.maintenance[task.RepositoryID])
		}
	}
}

func TestApplyReportsPartialFailureWithoutRollingBackAndRetryIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
	secrets := &setupSecrets{}
	initializer := &setupInitializer{storage: storage, fail: make(map[string]int)}
	service := setupService(storage, secrets, initializer, now)
	draft, err := service.CreateDraft(t.Context(), databaseDraftInput(now))
	if err != nil {
		t.Fatal(err)
	}
	initializer.fail[draft.Items[0].RepositoryID] = 1
	partial, err := service.Apply(t.Context(), draft.ID, nil)
	if err == nil || partial.Status != DraftPartial || len(storage.repositories) != 2 || len(storage.tasks) != 1 {
		t.Fatalf("partial=%+v err=%v repositories=%d tasks=%d", partial, err, len(storage.repositories), len(storage.tasks))
	}
	if partial.Items[0].Status != ItemFailed || partial.Items[0].Error == "" || partial.Items[1].Status != ItemReady {
		t.Fatalf("itemized result=%+v", partial.Items)
	}
	createdBefore := append([]string(nil), storage.createOrder...)
	ready, err := service.Apply(t.Context(), draft.ID, nil)
	if err != nil || ready.Status != DraftReady || len(storage.repositories) != 2 || len(storage.tasks) != 2 {
		t.Fatalf("retry result=%+v err=%v", ready, err)
	}
	for _, entry := range storage.createOrder[len(createdBefore):] {
		if strings.HasPrefix(entry, "repository:") {
			t.Fatalf("retry duplicated repository: before=%v after=%v", createdBefore, storage.createOrder)
		}
	}
}

func TestCancelDeletesOnlySecretsNotYetOwnedByCreatedRepositories(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
	secrets := &setupSecrets{}
	initializer := &setupInitializer{storage: storage, fail: make(map[string]int)}
	service := setupService(storage, secrets, initializer, now)
	draft, err := service.CreateDraft(t.Context(), databaseDraftInput(now))
	if err != nil {
		t.Fatal(err)
	}
	internal := storage.drafts[draft.ID]
	unownedSecretID := internal.Items[1].PasswordSecretID
	storage.repositories[internal.Items[0].RepositoryID] = domain.Repository{ID: internal.Items[0].RepositoryID, Status: "uninitialized"}
	cancelled, err := service.Cancel(t.Context(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != DraftCancelled || cancelled.Items[0].Status != ItemRetained || cancelled.Items[1].Status != ItemCancelled {
		t.Fatalf("cancelled=%+v", cancelled)
	}
	if !slices.Equal(secrets.deleted, []string{unownedSecretID}) {
		t.Fatalf("deleted secrets=%v", secrets.deleted)
	}
}

func TestCancelFinishesUnownedSecretCleanupAfterRequestCancellation(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	storage.connections = []domain.DatabaseConnection{{ID: "connection", Purpose: domain.BackupConnection, Status: "ready", Preflight: domain.DatabasePreflight{CheckedAt: now}}}
	ctx, cancel := context.WithCancel(t.Context())
	secrets := &cancellingCleanupSecrets{setupSecrets: &setupSecrets{}, cancel: cancel}
	service := setupService(storage, secrets, &setupInitializer{storage: storage}, now)
	draft, err := service.CreateDraft(ctx, databaseDraftInput(now))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(ctx, draft.ID)
	if err != nil {
		t.Fatalf("cancel draft after request cancellation: %v", err)
	}
	if cancelled.Status != DraftCancelled || len(secrets.deleted) != len(draft.Items) {
		t.Fatalf("cancelled=%+v deleted=%v", cancelled, secrets.deleted)
	}
}

func TestTemplateContainsOnlyReusablePolicyAndCannotSupplyResourceBindings(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	storage := newSetupStorage()
	service := setupService(storage, &setupSecrets{}, &setupInitializer{storage: storage}, now)
	template, err := service.CreateTemplate(t.Context(), TemplateInput{
		Name: "Daily", Retention: domain.RetentionPolicy{KeepDaily: 7}, Resources: domain.ResourcePolicy{Compression: "auto"},
		Schedule: domain.Schedule{Kind: domain.DailySchedule, TimeOfDay: "02:00"}, Timezone: "UTC", MaxParallel: 1, CatchUpWindowMinutes: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"password", "privateKey", "repositoryId", "repositoryPath", "source", "taskId"} {
		if strings.Contains(strings.ToLower(encoded), strings.ToLower(`"`+forbidden+`"`)) {
			t.Fatalf("template contains forbidden binding %q: %s", forbidden, encoded)
		}
	}
}
