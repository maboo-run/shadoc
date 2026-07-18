package s3backend

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/maboo-run/shadoc/internal/domain"
	"github.com/maboo-run/shadoc/internal/restic"
)

const CredentialPurpose = "s3-credentials"

type Credentials struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

func EncodeCredentials(value Credentials) ([]byte, error) {
	if err := value.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func DecodeCredentials(value []byte) (Credentials, error) {
	var result Credentials
	if json.Unmarshal(value, &result) != nil {
		return Credentials{}, errors.New("decode S3 credentials")
	}
	if err := result.validate(); err != nil {
		return Credentials{}, err
	}
	return result, nil
}

func (c Credentials) validate() error {
	if c.AccessKey == "" || c.SecretKey == "" || len(c.AccessKey) > 256 || len(c.SecretKey) > 1024 || strings.ContainsAny(c.AccessKey+c.SecretKey, "\x00\r\n") {
		return errors.New("S3 access key and secret key are required and must not contain control characters")
	}
	return nil
}

func Material(repository domain.Repository, password string, credentials Credentials) (restic.Repository, error) {
	if password == "" || repository.EffectiveKind() != domain.S3Repository || repository.S3 == nil {
		return restic.Repository{}, errors.New("S3 repository settings and password are required")
	}
	if err := repository.Validate(); err != nil {
		return restic.Repository{}, err
	}
	if err := credentials.validate(); err != nil {
		return restic.Repository{}, err
	}
	location := "s3:" + strings.TrimSuffix(repository.S3.Endpoint, "/") + "/" + repository.S3.Bucket
	if repository.Path != "" {
		location += "/" + repository.Path
	}
	lookup := "dns"
	if repository.S3.PathStyle {
		lookup = "path"
	}
	return restic.Repository{
		Location: location, Password: password, S3AccessKey: credentials.AccessKey, S3SecretKey: credentials.SecretKey,
		S3Region: repository.S3.Region, S3BucketLookup: lookup,
	}, nil
}
