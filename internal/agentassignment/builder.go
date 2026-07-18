package agentassignment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/maboo-run/shadoc/internal/agentrestore"
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/repositorycapacity"
	"github.com/maboo-run/shadoc/internal/restic"
	"github.com/maboo-run/shadoc/internal/resticagent"
	"github.com/maboo-run/shadoc/internal/rsync"
	"github.com/maboo-run/shadoc/internal/s3backend"
	"github.com/maboo-run/shadoc/internal/store"
)

type Storage interface {
	LoadTaskExecution(context.Context, string) (store.TaskExecution, error)
	LoadRsyncExecution(context.Context, string) (store.RsyncExecution, error)
	LoadRepositoryExecution(context.Context, string) (store.RepositoryExecution, error)
}

type Secrets interface {
	Get(context.Context, string, string) ([]byte, error)
}

type Builder struct {
	store   Storage
	secrets Secrets
}

func New(storage Storage, secrets Secrets) *Builder {
	return &Builder{store: storage, secrets: secrets}
}

func (b *Builder) Build(ctx context.Context, lease store.AgentLease) (json.RawMessage, error) {
	if lease.Engine == string(agentrestore.Kind) {
		var definition agentrestore.Definition
		if !json.Valid(lease.Definition) || json.Unmarshal(lease.Definition, &definition) != nil || definition.RepositoryID == "" {
			return nil, errors.New("invalid Agent restore lease definition")
		}
		aggregate, err := b.store.LoadRepositoryExecution(ctx, definition.RepositoryID)
		if err != nil {
			return nil, err
		}
		definition.Repository, err = b.repositoryMaterial(ctx, aggregate.Repository, aggregate.Host, aggregate.RepositoryPasswordSecretID, aggregate.PrivateKeySecretID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(definition)
	}
	if lease.Engine == string(repositorycapacity.Kind) {
		aggregate, err := b.store.LoadTaskExecution(ctx, lease.TaskID)
		if err != nil {
			return nil, err
		}
		if aggregate.Repository.EffectiveKind() == domain.S3Repository {
			return nil, errors.New("S3 repository capacity is not available from filesystem probes")
		}
		definition := repositorycapacity.Definition{Kind: string(aggregate.Repository.EffectiveKind()), Path: aggregate.Repository.Path}
		if aggregate.Repository.EffectiveKind() == domain.SFTPRepository {
			key, err := b.secrets.Get(ctx, aggregate.PrivateKeySecretID, "ssh-private-key")
			if err != nil {
				return nil, err
			}
			defer clear(key)
			definition.Host, definition.Port, definition.Username = aggregate.Host.Host, aggregate.Host.Port, aggregate.Host.Username
			definition.PrivateKey, definition.KnownHosts = string(key), aggregate.Host.HostFingerprint
		}
		if err := definition.Validate(); err != nil {
			return nil, err
		}
		return json.Marshal(definition)
	}
	switch domain.EngineKind(lease.Engine) {
	case domain.RsyncEngine:
		aggregate, err := b.store.LoadRsyncExecution(ctx, lease.TaskID)
		if err != nil {
			return nil, err
		}
		var control struct {
			DryRun bool `json:"dryRun"`
		}
		if len(lease.Definition) != 0 && json.Unmarshal(lease.Definition, &control) != nil {
			return nil, errors.New("invalid rsync lease control definition")
		}
		var key []byte
		if aggregate.PrivateKeySecretID != "" {
			key, err = b.secrets.Get(ctx, aggregate.PrivateKeySecretID, "ssh-private-key")
			if err != nil {
				return nil, err
			}
			defer clear(key)
		}
		definition := rsync.DefinitionFromExecution(aggregate, key)
		definition.DryRun = control.DryRun
		return json.Marshal(definition)
	case domain.ResticEngine:
		aggregate, err := b.store.LoadTaskExecution(ctx, lease.TaskID)
		if err != nil {
			return nil, err
		}
		if aggregate.Task.Kind != domain.DirectoryTask || aggregate.Task.Directory == nil {
			return nil, errors.New("agent restic currently requires a directory source")
		}
		repository, err := b.repositoryMaterial(ctx, aggregate.Repository, aggregate.Host, aggregate.RepositoryPasswordSecretID, aggregate.PrivateKeySecretID)
		if err != nil {
			return nil, err
		}
		directory := aggregate.Task.Directory
		arguments := make([]string, 0, 4)
		if aggregate.Task.Resources.UploadKiBPerSecond > 0 {
			arguments = append(arguments, "--limit-upload", fmt.Sprint(aggregate.Task.Resources.UploadKiBPerSecond))
		}
		if aggregate.Task.Resources.ReadConcurrency > 0 {
			arguments = append(arguments, "--read-concurrency", fmt.Sprint(aggregate.Task.Resources.ReadConcurrency))
		}
		return json.Marshal(resticagent.Definition{Repository: repository, Directory: restic.DirectoryBackup{Path: directory.Path, Exclusions: directory.Exclusions, SkipIfUnchanged: directory.SkipIfUnchanged, Compression: aggregate.Task.Resources.Compression}, Arguments: arguments})
	default:
		return nil, fmt.Errorf("unsupported agent engine %q", lease.Engine)
	}
}

func (b *Builder) repositoryMaterial(ctx context.Context, repository domain.Repository, host domain.RemoteHost, passwordSecretID, privateKeySecretID string) (restic.Repository, error) {
	password, err := b.secrets.Get(ctx, passwordSecretID, "repository-password")
	if err != nil {
		return restic.Repository{}, err
	}
	defer clear(password)
	material := restic.Repository{Location: repository.Path, Password: string(password)}
	if repository.EffectiveKind() == domain.S3Repository {
		encoded, err := b.secrets.Get(ctx, repository.BackendSecretID, s3backend.CredentialPurpose)
		if err != nil {
			return restic.Repository{}, err
		}
		defer clear(encoded)
		credentials, err := s3backend.DecodeCredentials(encoded)
		if err != nil {
			return restic.Repository{}, err
		}
		return s3backend.Material(repository, string(password), credentials)
	}
	if repository.EffectiveKind() == domain.SFTPRepository {
		key, err := b.secrets.Get(ctx, privateKeySecretID, "ssh-private-key")
		if err != nil {
			return restic.Repository{}, err
		}
		defer clear(key)
		hostName := host.Host
		if strings.Contains(hostName, ":") {
			hostName = "[" + strings.Trim(hostName, "[]") + "]"
		}
		material = restic.Repository{Location: fmt.Sprintf("sftp:%s@%s:%s", host.Username, hostName, repository.Path), Password: string(password), SSHPrivateKey: append([]byte(nil), key...), SSHPort: host.Port, KnownHosts: []byte(host.HostFingerprint)}
	}
	return material, nil
}
