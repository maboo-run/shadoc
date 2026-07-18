package rsync

import (
	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/store"
)

// DefinitionFromExecution is the single mapping from persisted task resources
// to the declarative rsync engine contract. Callers decide whether the same
// definition is a dry-run by setting DryRun after this mapping.
func DefinitionFromExecution(aggregate store.RsyncExecution, privateKey []byte) Definition {
	source := aggregate.Task.Rsync
	definition := Definition{SourcePath: source.Path, Exclusions: append([]string(nil), source.Exclusions...), Delete: source.Delete}
	if aggregate.Task.RepositoryID != "" {
		if aggregate.Repository.EffectiveKind() == domain.LocalRepository {
			definition.Destination = Destination{Kind: DestinationLocal, Path: aggregate.Repository.Path}
			return definition
		}
		definition.Destination = Destination{Kind: DestinationSSH, Host: aggregate.Host.Host, Port: aggregate.Host.Port, Username: aggregate.Host.Username, Path: aggregate.Repository.Path, PrivateKey: string(privateKey), KnownHosts: aggregate.Host.HostFingerprint}
		return definition
	}
	if source.EffectiveDestinationKind() == domain.RsyncDestinationLocal {
		definition.Destination = Destination{Kind: DestinationLocal, Path: source.DestinationPath}
		return definition
	}
	definition.Destination = Destination{Kind: DestinationSSH, Host: aggregate.Host.Host, Port: aggregate.Host.Port, Username: aggregate.Host.Username, Path: source.DestinationPath, PrivateKey: string(privateKey), KnownHosts: aggregate.Host.HostFingerprint}
	return definition
}
