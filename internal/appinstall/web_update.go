package appinstall

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"regexp"
)

var managedOperationIDPattern = regexp.MustCompile(`^op_[0-9a-f]{24}$`)

func ValidManagedOperationID(id string) bool { return managedOperationIDPattern.MatchString(id) }

type HelperStarter func(operationID, executable string, arguments []string) error

type WebUpdater struct {
	executable string
	dataDir    string
	listen     string
	managed    bool
	start      HelperStarter
}

func NewWebUpdater(executable, dataDir, listen string, managed bool, start HelperStarter) *WebUpdater {
	return &WebUpdater{executable: executable, dataDir: dataDir, listen: listen, managed: managed, start: start}
}

func (u *WebUpdater) Managed() bool { return u != nil && u.managed }

func (u *WebUpdater) Launch(ctx context.Context, operationID, version string) error {
	if u == nil || !u.managed {
		return errors.New("application is not running from the managed installation path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.start == nil || !managedOperationIDPattern.MatchString(operationID) || !releaseVersionPattern.MatchString(version) || !filepath.IsAbs(u.executable) || !filepath.IsAbs(u.dataDir) {
		return errors.New("managed application update configuration is invalid")
	}
	if _, _, err := net.SplitHostPort(u.listen); err != nil {
		return errors.New("managed application update listen address is invalid")
	}
	arguments := []string{
		"managed-update",
		"--operation-id", operationID,
		"--version", version,
		"--data-dir", u.dataDir,
		"--listen", u.listen,
	}
	return u.start(operationID, u.executable, arguments)
}
