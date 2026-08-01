package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStartCommandRegistersBackgroundServiceWithRequestedPort(t *testing.T) {
	service := &backgroundServiceFake{}
	var stdout bytes.Buffer

	handled, err := handleServiceCommand([]string{"start", "--port", "9090"}, &stdout, "/opt/shadoc", service, testLaunchConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("start command was not handled")
	}
	if service.executable != "/opt/shadoc" || !reflect.DeepEqual(service.arguments, []string{"serve", "--listen", "10.0.0.5:9090", "--data-dir", "/srv/shadoc"}) {
		t.Fatalf("executable=%q arguments=%v", service.executable, service.arguments)
	}
	if stdout.String() != "Shadoc started in the background on port 9090\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestStartWithoutPortPreservesConfiguredListenAddress(t *testing.T) {
	service := &backgroundServiceFake{}
	var stdout bytes.Buffer
	handled, err := handleServiceCommand([]string{"start"}, &stdout, "/opt/shadoc", service, testLaunchConfig)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if service.executable != "/opt/shadoc" || !reflect.DeepEqual(service.arguments, []string{"serve", "--listen", "10.0.0.5:8585", "--data-dir", "/srv/shadoc"}) {
		t.Fatalf("executable=%q arguments=%v", service.executable, service.arguments)
	}
	if stdout.String() != "Shadoc started in the background\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSystemLifecycleCommandsUseExplicitSystemScope(t *testing.T) {
	service := &backgroundServiceFake{status: "running"}
	var stdout bytes.Buffer
	handled, err := handleServiceCommand([]string{"start", "--system"}, &stdout, "/var/lib/shadoc/app/shadoc", service, testSystemLaunchConfig)
	if err != nil || !handled {
		t.Fatalf("start handled=%t err=%v", handled, err)
	}
	want := []string{"serve", "--service-scope", "system", "--listen", "127.0.0.1:8585", "--data-dir", "/var/lib/shadoc"}
	if service.executable != "/var/lib/shadoc/app/shadoc" || !reflect.DeepEqual(service.arguments, want) {
		t.Fatalf("executable=%q arguments=%v", service.executable, service.arguments)
	}
	for _, command := range []string{"stop", "restart", "status"} {
		handled, err := handleServiceCommand([]string{command, "--system"}, &stdout, "/var/lib/shadoc/app/shadoc", service, testSystemLaunchConfig)
		if err != nil || !handled {
			t.Fatalf("command=%s handled=%t err=%v", command, handled, err)
		}
	}
}

func TestStartPrintsPersistedLANSetupToken(t *testing.T) {
	dataDir := t.TempDir()
	token := "MTExMTExMTExMTExMTExMTExMTExMTEx"
	if err := os.WriteFile(filepath.Join(dataDir, setupTokenFilename), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &backgroundServiceFake{}
	var stdout bytes.Buffer
	handled, err := handleServiceCommand([]string{"start"}, &stdout, "/opt/shadoc", service, func() (serviceLaunchConfig, error) {
		return serviceLaunchConfig{DataDir: dataDir, Listen: "0.0.0.0:8585"}, nil
	})
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if !strings.Contains(stdout.String(), "LAN initialization token: "+token) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestUnknownCommandReturnsAnErrorInsteadOfStartingServer(t *testing.T) {
	handled, err := handleServiceCommand([]string{"strat"}, ioDiscard{}, "/opt/shadoc", &backgroundServiceFake{}, testLaunchConfig)
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown Shadoc command") {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
}

func TestVersionCommandPrintsBuildVersionWithoutOpeningTheService(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		var stdout bytes.Buffer
		handled, err := handleVersionCommand(args, &stdout, "0.1.4")
		if err != nil || !handled || stdout.String() != "0.1.4\n" {
			t.Fatalf("args=%v handled=%t err=%v stdout=%q", args, handled, err, stdout.String())
		}
	}
	if handled, err := handleVersionCommand([]string{"--version", "unexpected"}, ioDiscard{}, "0.1.4"); !handled || err == nil {
		t.Fatalf("version arguments handled=%t err=%v", handled, err)
	}
}

func TestLifecycleCommandsDispatchThroughBackgroundService(t *testing.T) {
	service := &backgroundServiceFake{status: "running"}
	var stdout bytes.Buffer
	for _, command := range []string{"stop", "restart", "status"} {
		handled, err := handleServiceCommand([]string{command}, &stdout, "/opt/shadoc", service, testLaunchConfig)
		if err != nil || !handled {
			t.Fatalf("command=%s handled=%t err=%v", command, handled, err)
		}
	}
	if service.stops != 1 || service.restarts != 1 || service.statusChecks != 1 {
		t.Fatalf("stops=%d restarts=%d statusChecks=%d", service.stops, service.restarts, service.statusChecks)
	}
	if stdout.String() != "Shadoc stopped\nShadoc restarted\nShadoc is running\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestHelpDescribesPublicShadocCommandsWithoutTouchingService(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		service := &backgroundServiceFake{}
		var stdout bytes.Buffer
		handled, err := handleServiceCommand(args, &stdout, "/opt/shadoc", service, testLaunchConfig)
		if err != nil || !handled {
			t.Fatalf("args=%v handled=%t err=%v", args, handled, err)
		}
		for _, expected := range []string{"shadoc start [--port PORT]", "shadoc stop", "shadoc restart", "shadoc status", "shadoc update-app", "shadoc uninstall-app", "shadoc --version", "shadoc help"} {
			if !strings.Contains(stdout.String(), expected) {
				t.Fatalf("args=%v help missing %q:\n%s", args, expected, stdout.String())
			}
		}
		if service.calls() != 0 {
			t.Fatalf("args=%v touched service", args)
		}
	}
}

func TestStartRejectsUnsafePortsBeforeTouchingService(t *testing.T) {
	for _, port := range []string{"0", "65536", "not-a-port"} {
		service := &backgroundServiceFake{}
		handled, err := handleServiceCommand([]string{"start", "--port", port}, ioDiscard{}, "/opt/shadoc", service, testLaunchConfig)
		if !handled || err == nil || service.calls() != 0 {
			t.Fatalf("port=%q handled=%t err=%v calls=%d", port, handled, err, service.calls())
		}
	}
}

func TestServiceCommandPropagatesNativeLifecycleErrors(t *testing.T) {
	service := &backgroundServiceFake{err: errors.New("native service unavailable")}
	handled, err := handleServiceCommand([]string{"stop"}, ioDiscard{}, "/opt/shadoc", service, testLaunchConfig)
	if !handled || !errors.Is(err, service.err) {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
}

func TestServeCommandReplacesOnlyConfiguredPort(t *testing.T) {
	options, handled, err := parseServeCommand([]string{"serve", "--listen", "0.0.0.0:8585", "--data-dir", "/srv/shadoc", "--port", "9090"})
	if err != nil || !handled || options.Port != 9090 || options.Listen != "0.0.0.0:8585" || options.DataDir != "/srv/shadoc" {
		t.Fatalf("options=%+v handled=%t err=%v", options, handled, err)
	}
	listen, err := listenWithPort(options.Listen, options.Port)
	if err != nil {
		t.Fatal(err)
	}
	if listen != "0.0.0.0:9090" {
		t.Fatalf("listen=%q", listen)
	}
}

func TestServiceCommandsAndServeCarryOnlyKnownScopes(t *testing.T) {
	for _, test := range []struct {
		args []string
		want serviceScope
	}{
		{args: []string{"start"}, want: userServiceScope},
		{args: []string{"start", "--system"}, want: systemServiceScope},
		{args: []string{"restart", "--system"}, want: systemServiceScope},
		{args: []string{"status"}, want: userServiceScope},
	} {
		got, err := serviceCommandScope(test.args)
		if err != nil || got != test.want {
			t.Fatalf("args=%v scope=%q err=%v", test.args, got, err)
		}
	}
	options, handled, err := parseServeCommand([]string{"serve", "--service-scope", "system", "--listen", "127.0.0.1:8585", "--data-dir", "/var/lib/shadoc"})
	if err != nil || !handled || options.ServiceScope != systemServiceScope {
		t.Fatalf("options=%+v handled=%t err=%v", options, handled, err)
	}
	if _, _, err := parseServeCommand([]string{"serve", "--service-scope", "administrator"}); err == nil {
		t.Fatal("unknown serve service scope accepted")
	}
}

func TestHelpAndUnknownCommandsStillReachTheBackgroundDispatcher(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"unknown"}} {
		scope, err := backgroundCommandScope(args)
		if err != nil || scope != userServiceScope {
			t.Fatalf("args=%v scope=%q err=%v", args, scope, err)
		}
	}
	if _, err := backgroundCommandScope([]string{"start", "--unknown"}); err == nil {
		t.Fatal("invalid start flags were ignored")
	}
}

func testLaunchConfig() (serviceLaunchConfig, error) {
	return serviceLaunchConfig{DataDir: "/srv/shadoc", Listen: "10.0.0.5:8585"}, nil
}

func testSystemLaunchConfig() (serviceLaunchConfig, error) {
	return serviceLaunchConfig{DataDir: "/var/lib/shadoc", Listen: "127.0.0.1:8585"}, nil
}

type backgroundServiceFake struct {
	executable   string
	arguments    []string
	stops        int
	restarts     int
	statusChecks int
	status       string
	err          error
}

func (s *backgroundServiceFake) Start(executable string, arguments []string) error {
	s.executable = executable
	s.arguments = append([]string(nil), arguments...)
	return s.err
}

func (s *backgroundServiceFake) Stop() error {
	s.stops++
	return s.err
}

func (s *backgroundServiceFake) Restart() error {
	s.restarts++
	return s.err
}

func (s *backgroundServiceFake) Status() (string, error) {
	s.statusChecks++
	return s.status, s.err
}

func (s *backgroundServiceFake) calls() int {
	count := s.stops + s.restarts + s.statusChecks
	if s.executable != "" {
		count++
	}
	return count
}

type ioDiscard struct{}

func (ioDiscard) Write(value []byte) (int, error) { return len(value), nil }
