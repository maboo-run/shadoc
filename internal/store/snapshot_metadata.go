package store

import (
	"context"
	"time"

	"github.com/maboo-run/shadoc/internal/database"
)

type SnapshotMetadataRecord struct {
	RepositoryID    string                    `json:"repositoryId"`
	SnapshotID      string                    `json:"snapshotId"`
	MetadataVersion int                       `json:"metadataVersion"`
	Metadata        database.SnapshotMetadata `json:"metadata"`
	CreatedAt       time.Time                 `json:"createdAt"`
}

func (s *Store) SaveSnapshotMetadata(ctx context.Context, repositoryID, snapshotID string, metadata database.SnapshotMetadata, createdAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO snapshot_metadata(repository_id,snapshot_id,metadata_version,engine,database_name,format,filename,server_version,client_version,encoding,collation,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repository_id,snapshot_id) DO UPDATE SET metadata_version=excluded.metadata_version,engine=excluded.engine,database_name=excluded.database_name,format=excluded.format,filename=excluded.filename,server_version=excluded.server_version,client_version=excluded.client_version,encoding=excluded.encoding,collation=excluded.collation,created_at=excluded.created_at`, repositoryID, snapshotID, 1, string(metadata.Engine), metadata.Database, metadata.Format, metadata.Filename, metadata.ServerVersion, metadata.ClientVersion, metadata.Encoding, metadata.Collation, formatTime(createdAt))
	return err
}

func (s *Store) SnapshotMetadata(ctx context.Context, repositoryID, snapshotID string) (SnapshotMetadataRecord, error) {
	var record SnapshotMetadataRecord
	var created string
	record.RepositoryID, record.SnapshotID = repositoryID, snapshotID
	err := s.db.QueryRowContext(ctx, `SELECT metadata_version,engine,database_name,format,filename,server_version,client_version,encoding,collation,created_at FROM snapshot_metadata WHERE repository_id=? AND snapshot_id=?`, repositoryID, snapshotID).Scan(&record.MetadataVersion, &record.Metadata.Engine, &record.Metadata.Database, &record.Metadata.Format, &record.Metadata.Filename, &record.Metadata.ServerVersion, &record.Metadata.ClientVersion, &record.Metadata.Encoding, &record.Metadata.Collation, &created)
	if err != nil {
		return record, err
	}
	record.CreatedAt, err = parseTime(created)
	return record, err
}
