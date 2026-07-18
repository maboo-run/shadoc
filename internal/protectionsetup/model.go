package protectionsetup

import (
	"errors"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

type DraftStatus string
type ItemStatus string
type NotificationMode string

const (
	DraftPending   DraftStatus = "pending"
	DraftApplying  DraftStatus = "applying"
	DraftPartial   DraftStatus = "partial"
	DraftReady     DraftStatus = "ready"
	DraftCancelled DraftStatus = "cancelled"

	ItemPending   ItemStatus = "pending"
	ItemFailed    ItemStatus = "failed"
	ItemReady     ItemStatus = "ready"
	ItemRetained  ItemStatus = "retained"
	ItemCancelled ItemStatus = "cancelled"

	NotificationConfigured NotificationMode = "configured"
	NotificationNone       NotificationMode = "none"
)

type Template struct {
	ID                   string                  `json:"id"`
	Name                 string                  `json:"name"`
	Retention            domain.RetentionPolicy  `json:"retention"`
	Resources            domain.ResourcePolicy   `json:"resources"`
	Health               domain.TaskHealthPolicy `json:"health"`
	Schedule             domain.Schedule         `json:"schedule"`
	Timezone             string                  `json:"timezone"`
	MaxParallel          int                     `json:"maxParallel"`
	CatchUpWindowMinutes int                     `json:"catchUpWindowMinutes"`
	CreatedAt            time.Time               `json:"createdAt"`
	UpdatedAt            time.Time               `json:"updatedAt"`
}

type TemplateInput struct {
	Name                 string                  `json:"name"`
	Retention            domain.RetentionPolicy  `json:"retention"`
	Resources            domain.ResourcePolicy   `json:"resources"`
	Health               domain.TaskHealthPolicy `json:"health"`
	Schedule             domain.Schedule         `json:"schedule"`
	Timezone             string                  `json:"timezone"`
	MaxParallel          int                     `json:"maxParallel"`
	CatchUpWindowMinutes int                     `json:"catchUpWindowMinutes"`
}

func (t Template) Validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 200 {
		return errors.New("template name is required")
	}
	if err := t.Retention.Validate(); err != nil {
		return err
	}
	if err := t.Resources.Validate(); err != nil {
		return err
	}
	if err := t.Health.Validate(); err != nil {
		return err
	}
	return (domain.Plan{Name: t.Name, Schedule: t.Schedule, Timezone: t.Timezone, MaxParallel: t.MaxParallel, TaskIDs: []string{"template"}, CatchUpWindowMinutes: t.CatchUpWindowMinutes}).Validate()
}

type Draft struct {
	ID                   string                  `json:"id"`
	Name                 string                  `json:"name"`
	TemplateID           string                  `json:"templateId,omitempty"`
	ExecutionTarget      execution.Target        `json:"executionTarget"`
	Retention            domain.RetentionPolicy  `json:"retention"`
	Resources            domain.ResourcePolicy   `json:"resources"`
	Health               domain.TaskHealthPolicy `json:"health"`
	Schedule             domain.Schedule         `json:"schedule"`
	Timezone             string                  `json:"timezone"`
	MaxParallel          int                     `json:"maxParallel"`
	CatchUpWindowMinutes int                     `json:"catchUpWindowMinutes"`
	NotificationMode     NotificationMode        `json:"notificationMode"`
	PlanID               string                  `json:"planId"`
	Status               DraftStatus             `json:"status"`
	Items                []DraftItem             `json:"items"`
	CreatedAt            time.Time               `json:"createdAt"`
	UpdatedAt            time.Time               `json:"updatedAt"`
}

type DraftItem struct {
	ID               string                  `json:"id"`
	DraftID          string                  `json:"draftId"`
	Position         int                     `json:"position"`
	TaskName         string                  `json:"taskName"`
	Directory        *domain.DirectorySource `json:"directory,omitempty"`
	Database         *domain.DatabaseSource  `json:"database,omitempty"`
	RepositoryID     string                  `json:"repositoryId"`
	RepositoryName   string                  `json:"repositoryName"`
	RepositoryKind   domain.RepositoryKind   `json:"repositoryKind"`
	RemoteHostID     string                  `json:"remoteHostId,omitempty"`
	RepositoryPath   string                  `json:"repositoryPath"`
	TaskID           string                  `json:"taskId"`
	Status           ItemStatus              `json:"status"`
	Error            string                  `json:"error,omitempty"`
	HasPassword      bool                    `json:"hasPassword"`
	PasswordSecretID string                  `json:"-"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

type CreateDraftInput struct {
	Name                 string                  `json:"name"`
	TemplateID           string                  `json:"templateId,omitempty"`
	ExecutionTarget      execution.Target        `json:"executionTarget"`
	Retention            domain.RetentionPolicy  `json:"retention"`
	Resources            domain.ResourcePolicy   `json:"resources"`
	Health               domain.TaskHealthPolicy `json:"health"`
	Schedule             domain.Schedule         `json:"schedule"`
	Timezone             string                  `json:"timezone"`
	MaxParallel          int                     `json:"maxParallel"`
	CatchUpWindowMinutes int                     `json:"catchUpWindowMinutes"`
	NotificationMode     NotificationMode        `json:"notificationMode"`
	Items                []CreateDraftItem       `json:"items"`
}

type CreateDraftItem struct {
	TaskName          string                  `json:"taskName"`
	Directory         *domain.DirectorySource `json:"directory,omitempty"`
	Database          *domain.DatabaseSource  `json:"database,omitempty"`
	RepositoryName    string                  `json:"repositoryName"`
	RepositoryKind    domain.RepositoryKind   `json:"repositoryKind"`
	RemoteHostID      string                  `json:"remoteHostId,omitempty"`
	RepositoryPath    string                  `json:"repositoryPath"`
	Password          string                  `json:"password"`
	PasswordConfirmed bool                    `json:"passwordConfirmed"`
}

type StageReporter func(string, map[string]any)
