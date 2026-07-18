package appinstall

import (
	"context"
	"fmt"
	"testing"
)

func TestWebUpdaterLaunchesOnlyFixedValidatedHelperArguments(t *testing.T) {
	var gotID, gotExecutable string
	var gotArguments []string
	updater := NewWebUpdater("/srv/shadoc/app/shadoc", "/srv/shadoc", "127.0.0.1:8585", true, func(id, executable string, arguments []string) error {
		gotID, gotExecutable, gotArguments = id, executable, append([]string(nil), arguments...)
		return nil
	})
	if err := updater.Launch(context.Background(), "op_0123456789abcdef01234567", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	want := "[managed-update --operation-id op_0123456789abcdef01234567 --version v1.2.3 --data-dir /srv/shadoc --listen 127.0.0.1:8585]"
	if gotID != "op_0123456789abcdef01234567" || gotExecutable != "/srv/shadoc/app/shadoc" || fmt.Sprint(gotArguments) != want {
		t.Fatalf("id=%q executable=%q arguments=%v", gotID, gotExecutable, gotArguments)
	}
	if err := updater.Launch(context.Background(), "op_0123456789abcdef01234567", "https://attacker.example/binary"); err == nil {
		t.Fatal("download URL was accepted as a version")
	}
}
