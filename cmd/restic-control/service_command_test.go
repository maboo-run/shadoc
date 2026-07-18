package main

import (
	"bytes"
	"errors"
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

func TestUnknownCommandReturnsAnErrorInsteadOfStartingServer(t *testing.T) {
	handled, err := handleServiceCommand([]string{"strat"}, ioDiscard{}, "/opt/shadoc", &backgroundServiceFake{}, testLaunchConfig)
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown Shadoc command") {
		t.Fatalf("handled=%t err=%v", handled, err)
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
		for _, expected := range []string{"shadoc start [--port PORT]", "shadoc stop", "shadoc restart", "shadoc status", "shadoc update-app", "shadoc uninstall-app", "shadoc help"} {
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

func testLaunchConfig() (serviceLaunchConfig, error) {
	return serviceLaunchConfig{DataDir: "/srv/shadoc", Listen: "10.0.0.5:8585"}, nil
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
