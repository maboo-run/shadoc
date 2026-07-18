package database

import (
	"context"
	"strings"
	"testing"
)

func TestParseMetadataReadsMySQLAndPostgresServerAndClientFacts(t *testing.T) {
	tests := []struct {
		name, server, client string
		engine               Engine
		want                 SnapshotMetadata
	}{
		{
			name: "mysql", engine: MySQL,
			server: "8.4.5\tutf8mb4\tutf8mb4_0900_ai_ci\n", client: "mysqldump  Ver 10.13 Distrib 8.4.5, for macos14 (arm64)",
			want: SnapshotMetadata{ServerVersion: "8.4.5", ClientVersion: "8.4.5", Encoding: "utf8mb4", Collation: "utf8mb4_0900_ai_ci"},
		},
		{
			name: "postgresql", engine: PostgreSQL,
			server: "17.4|UTF8|en_US.UTF-8\n", client: "pg_dump (PostgreSQL) 17.4",
			want: SnapshotMetadata{ServerVersion: "17.4", ClientVersion: "17.4", Encoding: "UTF8", Collation: "en_US.UTF-8"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseMetadata(test.engine, test.server, test.client)
			if err != nil {
				t.Fatal(err)
			}
			if got.ServerVersion != test.want.ServerVersion || got.ClientVersion != test.want.ClientVersion || got.Encoding != test.want.Encoding || got.Collation != test.want.Collation {
				t.Fatalf("metadata=%+v", got)
			}
		})
	}
}

func TestConnectorsPrepareCredentialBoundMetadataCommands(t *testing.T) {
	tests := []struct {
		name       string
		connector  MetadataConnector
		connection Connection
		wantQuery  string
	}{
		{
			name: "mysql", connector: NewMySQL(t.TempDir()).(MetadataConnector),
			connection: Connection{Engine: MySQL, Purpose: Backup, Network: TCP, Host: "db", Port: 3306, Username: "backup", Password: "secret", DumpProgram: "/tools/mysqldump", AdminProgram: "/tools/mysql"},
			wantQuery:  "@@character_set_database",
		},
		{
			name: "postgresql", connector: NewPostgres(t.TempDir()).(MetadataConnector),
			connection: Connection{Engine: PostgreSQL, Purpose: Backup, Network: TCP, Host: "db", Port: 5432, Username: "backup", Password: "secret", DumpProgram: "/tools/pg_dump", AdminProgram: "/tools/psql"},
			wantQuery:  "server_encoding",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			export, _, err := test.connector.PrepareExport(context.Background(), test.connection, "gitea")
			if err != nil {
				t.Fatal(err)
			}
			defer export.Cleanup()
			probe, err := test.connector.PrepareMetadata(context.Background(), test.connection, "gitea", export.CredentialPath)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(probe.Server.Args, " ")
			if !strings.Contains(joined, test.wantQuery) || strings.Contains(joined, "secret") || probe.Client.Program != test.connection.DumpProgram {
				t.Fatalf("probe=%+v", probe)
			}
		})
	}
}

func TestParseMetadataRejectsIncompleteOrUnknownOutput(t *testing.T) {
	for _, test := range []struct {
		engine Engine
		server string
		client string
	}{
		{engine: MySQL, server: "8.4.5\tutf8mb4\n", client: "mysqldump  Ver 8.4.5"},
		{engine: PostgreSQL, server: "17.4|UTF8|\n", client: "pg_dump (PostgreSQL) 17.4"},
		{engine: "sqlite", server: "3|UTF8|binary", client: "sqlite 3"},
	} {
		if _, err := ParseMetadata(test.engine, test.server, test.client); err == nil {
			t.Fatalf("accepted engine=%s server=%q", test.engine, test.server)
		}
	}
}

func TestMetadataTagsRoundTripValuesWithSeparatorsAndUnicode(t *testing.T) {
	want := SnapshotMetadata{Engine: PostgreSQL, Database: "业务 库", Format: "postgres-custom", Filename: "业务.dump", ServerVersion: "17.4", ClientVersion: "17.4", Encoding: "UTF8", Collation: "zh_CN.UTF-8@collation=v2"}
	tags, err := EncodeMetadataTags(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMetadataTags(tags)
	if err != nil || got != want {
		t.Fatalf("metadata=%+v tags=%v err=%v", got, tags, err)
	}
	for _, tag := range tags {
		if strings.Contains(tag, "业务") || strings.Contains(tag, " ") {
			t.Fatalf("tag value was not safely encoded: %q", tag)
		}
	}
}

func TestRestoreClientCompatibilityRejectsOlderMajorAndAcceptsSameOrNewer(t *testing.T) {
	metadata := SnapshotMetadata{Engine: PostgreSQL, ClientVersion: "17.4"}
	if err := CheckRestoreClientCompatibility(metadata, "pg_restore (PostgreSQL) 16.9"); err == nil {
		t.Fatal("older restore client accepted")
	}
	for _, output := range []string{"pg_restore (PostgreSQL) 17.0", "pg_restore (PostgreSQL) 18.1"} {
		if err := CheckRestoreClientCompatibility(metadata, output); err != nil {
			t.Fatalf("compatible output %q rejected: %v", output, err)
		}
	}
}

func TestRestoreClientCompatibilityUsesMySQLDistributionVersion(t *testing.T) {
	metadata := SnapshotMetadata{Engine: MySQL, ClientVersion: "8.4.5"}
	if err := CheckRestoreClientCompatibility(metadata, "mysql  Ver 10.13 Distrib 8.0.42, for macos14 (arm64)"); err != nil {
		t.Fatalf("matching Oracle MySQL client rejected: %v", err)
	}
}
