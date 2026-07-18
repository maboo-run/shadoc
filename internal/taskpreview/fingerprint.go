// Package taskpreview binds an administrator-reviewed source preview to the
// exact task scope that will be executed.
package taskpreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/execution"
)

type fingerprintScope struct {
	Engine          domain.EngineKind     `json:"engine"`
	Kind            domain.TaskKind       `json:"kind"`
	ExecutionTarget execution.Target      `json:"executionTarget"`
	RepositoryID    string                `json:"repositoryId,omitempty"`
	Directory       *fingerprintDirectory `json:"directory,omitempty"`
	Rsync           *fingerprintRsync     `json:"rsync,omitempty"`
}

type fingerprintDirectory struct {
	Path            string   `json:"path"`
	Exclusions      []string `json:"exclusions"`
	SkipIfUnchanged bool     `json:"skipIfUnchanged"`
}

type fingerprintRsync struct {
	Path              string                      `json:"path"`
	DestinationKind   domain.RsyncDestinationKind `json:"destinationKind"`
	DestinationHostID string                      `json:"destinationHostId,omitempty"`
	DestinationPath   string                      `json:"destinationPath,omitempty"`
	Exclusions        []string                    `json:"exclusions"`
	Delete            bool                        `json:"delete"`
}

// Fingerprint returns a stable digest of fields that can change which source
// data is protected or which rsync target is modified. Display and scheduling
// fields are deliberately excluded.
func Fingerprint(task domain.Task) (string, error) {
	scope := fingerprintScope{
		Engine:          task.EffectiveEngine(),
		Kind:            task.Kind,
		ExecutionTarget: task.EffectiveExecutionTarget(),
		RepositoryID:    task.RepositoryID,
	}
	if task.Directory != nil {
		scope.Directory = &fingerprintDirectory{
			Path:            task.Directory.Path,
			Exclusions:      normalizedStrings(task.Directory.Exclusions),
			SkipIfUnchanged: task.Directory.SkipIfUnchanged,
		}
	}
	if task.Rsync != nil {
		scope.Rsync = &fingerprintRsync{
			Path:              task.Rsync.Path,
			DestinationKind:   task.Rsync.EffectiveDestinationKind(),
			DestinationHostID: task.Rsync.DestinationHostID,
			DestinationPath:   task.Rsync.DestinationPath,
			Exclusions:        normalizedStrings(task.Rsync.Exclusions),
			Delete:            task.Rsync.Delete,
		}
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func normalizedStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}
