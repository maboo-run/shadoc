package database

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var metadataVersionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
var mysqlDistributionVersionPattern = regexp.MustCompile(`(?i)\bDistrib\s+(\d+\.\d+(?:\.\d+)?)`)

func ParseMetadata(engine Engine, serverOutput, clientOutput string) (SnapshotMetadata, error) {
	separator := "\t"
	if engine == PostgreSQL {
		separator = "|"
	} else if engine != MySQL {
		return SnapshotMetadata{}, errors.New("unsupported database metadata engine")
	}
	fields := strings.Split(strings.TrimSpace(serverOutput), separator)
	if len(fields) != 3 {
		return SnapshotMetadata{}, errors.New("database metadata query returned an unexpected shape")
	}
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
		if fields[index] == "" {
			return SnapshotMetadata{}, errors.New("database metadata contains an empty value")
		}
	}
	clientVersion := parseClientVersion(engine, clientOutput)
	if clientVersion == "" {
		return SnapshotMetadata{}, errors.New("database client version was not recognized")
	}
	return SnapshotMetadata{Engine: engine, ServerVersion: fields[0], ClientVersion: clientVersion, Encoding: fields[1], Collation: fields[2]}, nil
}

func parseClientVersion(engine Engine, output string) string {
	if engine == MySQL {
		if match := mysqlDistributionVersionPattern.FindStringSubmatch(output); len(match) == 2 {
			return match[1]
		}
	}
	return metadataVersionPattern.FindString(output)
}

var metadataTagFields = []struct {
	name string
	read func(SnapshotMetadata) string
}{
	{"engine", func(m SnapshotMetadata) string { return string(m.Engine) }},
	{"database", func(m SnapshotMetadata) string { return m.Database }},
	{"format", func(m SnapshotMetadata) string { return m.Format }},
	{"filename", func(m SnapshotMetadata) string { return m.Filename }},
	{"server-version", func(m SnapshotMetadata) string { return m.ServerVersion }},
	{"client-version", func(m SnapshotMetadata) string { return m.ClientVersion }},
	{"encoding", func(m SnapshotMetadata) string { return m.Encoding }},
	{"collation", func(m SnapshotMetadata) string { return m.Collation }},
}

func EncodeMetadataTags(metadata SnapshotMetadata) ([]string, error) {
	if err := validateCompleteMetadata(metadata); err != nil {
		return nil, err
	}
	tags := []string{"rc:metadata-version=1", "rc:source=database"}
	for _, field := range metadataTagFields {
		tags = append(tags, "rc:meta-"+field.name+"="+base64.RawURLEncoding.EncodeToString([]byte(field.read(metadata))))
	}
	return tags, nil
}

func DecodeMetadataTags(tags []string) (SnapshotMetadata, error) {
	values := map[string]string{}
	version := ""
	for _, tag := range tags {
		key, value, ok := strings.Cut(tag, "=")
		if !ok {
			continue
		}
		if key == "rc:metadata-version" {
			version = value
			continue
		}
		if !strings.HasPrefix(key, "rc:meta-") {
			continue
		}
		name := strings.TrimPrefix(key, "rc:meta-")
		if _, duplicate := values[name]; duplicate {
			return SnapshotMetadata{}, fmt.Errorf("duplicate database metadata tag %q", name)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return SnapshotMetadata{}, fmt.Errorf("decode database metadata tag %q: %w", name, err)
		}
		values[name] = string(decoded)
	}
	if version != "1" {
		return SnapshotMetadata{}, fmt.Errorf("unsupported database metadata version %q", version)
	}
	metadata := SnapshotMetadata{Engine: Engine(values["engine"]), Database: values["database"], Format: values["format"], Filename: values["filename"], ServerVersion: values["server-version"], ClientVersion: values["client-version"], Encoding: values["encoding"], Collation: values["collation"]}
	if err := validateCompleteMetadata(metadata); err != nil {
		return SnapshotMetadata{}, err
	}
	return metadata, nil
}

func validateCompleteMetadata(metadata SnapshotMetadata) error {
	if metadata.Engine != MySQL && metadata.Engine != PostgreSQL {
		return errors.New("database snapshot metadata has an unsupported engine")
	}
	for _, field := range metadataTagFields {
		if strings.TrimSpace(field.read(metadata)) == "" {
			return fmt.Errorf("database snapshot metadata %s is required", field.name)
		}
	}
	return nil
}

func CheckRestoreClientCompatibility(metadata SnapshotMetadata, restoreClientOutput string) error {
	current := parseClientVersion(metadata.Engine, restoreClientOutput)
	if current == "" {
		return errors.New("restore client version was not recognized")
	}
	snapshotMajor, err := versionMajor(metadata.ClientVersion)
	if err != nil {
		return fmt.Errorf("invalid snapshot client version: %w", err)
	}
	currentMajor, err := versionMajor(current)
	if err != nil {
		return fmt.Errorf("invalid restore client version: %w", err)
	}
	if currentMajor < snapshotMajor {
		return fmt.Errorf("restore client major version %d is older than snapshot client major version %d", currentMajor, snapshotMajor)
	}
	return nil
}

func versionMajor(version string) (int, error) {
	value := strings.SplitN(version, ".", 2)[0]
	major, err := strconv.Atoi(value)
	if err != nil || major < 1 {
		return 0, errors.New("version major is invalid")
	}
	return major, nil
}
