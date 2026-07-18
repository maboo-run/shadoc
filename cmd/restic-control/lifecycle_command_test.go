package main

import (
	"bytes"
	"context"
	"testing"
)

type fakeLifecycle struct {
	installed string
	updated   string
	uninstall *bool
}

func (f *fakeLifecycle) InstallCurrent(_ context.Context, current string) error {
	f.installed = current
	return nil
}

func (f *fakeLifecycle) Update(_ context.Context, version string) error {
	f.updated = version
	return nil
}

func (f *fakeLifecycle) Uninstall(removeData bool) error {
	f.uninstall = &removeData
	return nil
}

func TestLifecycleCommandRoutesInstallAndSelectedUpdateVersion(t *testing.T) {
	for _, test := range []struct {
		args  []string
		check func(*fakeLifecycle) bool
	}{
		{[]string{"install-app"}, func(f *fakeLifecycle) bool { return f.installed == "/tmp/current" }},
		{[]string{"update-app", "--version", "1.2.3"}, func(f *fakeLifecycle) bool { return f.updated == "1.2.3" }},
	} {
		lifecycle := &fakeLifecycle{}
		handled, err := handleLifecycleCommand(context.Background(), test.args, bytes.NewBuffer(nil), &bytes.Buffer{}, lifecycle, "/tmp/current")
		if err != nil || !handled || !test.check(lifecycle) {
			t.Fatalf("args=%v handled=%v err=%v lifecycle=%+v", test.args, handled, err, lifecycle)
		}
	}
}

func TestRemoveDataRequiresExplicitInteractiveConfirmation(t *testing.T) {
	for _, test := range []struct {
		input     string
		wantError bool
		called    bool
	}{
		{"no\n", true, false},
		{"REMOVE\n", false, true},
	} {
		lifecycle := &fakeLifecycle{}
		handled, err := handleLifecycleCommand(context.Background(), []string{"uninstall-app", "--remove-data"}, bytes.NewBufferString(test.input), &bytes.Buffer{}, lifecycle, "/tmp/current")
		if !handled || (err != nil) != test.wantError || (lifecycle.uninstall != nil) != test.called {
			t.Fatalf("input=%q handled=%v err=%v called=%v", test.input, handled, err, lifecycle.uninstall)
		}
		if test.called && !*lifecycle.uninstall {
			t.Fatal("remove-data confirmation was lost")
		}
	}
}

func TestUnknownCommandIsNotHandled(t *testing.T) {
	handled, err := handleLifecycleCommand(context.Background(), []string{"serve"}, bytes.NewBuffer(nil), &bytes.Buffer{}, &fakeLifecycle{}, "/tmp/current")
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
