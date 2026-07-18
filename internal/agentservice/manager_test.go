package agentservice

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
	"github.com/maboo-run/shadoc/internal/vault"
)

func TestValidateRequiresSafeUserServicePortAndReachableName(t *testing.T) {
	for _, settings := range []Settings{
		{Enabled: true, ListenHost: "0.0.0.0", Port: 443, AdvertisedHost: "control.lan"},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "0.0.0.0"},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "https://control.lan"},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "foo_bar"},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "-control.lan"},
		{Enabled: true, ListenHost: "0.0.0.0", Port: 9443, AdvertisedHost: "control..lan"},
	} {
		if err := Validate(settings); err == nil {
			t.Fatalf("accepted unsafe settings: %+v", settings)
		}
	}
	if err := Validate(Settings{Enabled: true, ListenHost: "0.0.0.0", Port: 10443, AdvertisedHost: "192.168.1.20"}); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
}

func TestSavingUnchangedFallbackEstablishesPersistentPageSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(s, secret.New(s, v, time.Now), dir, filepath.Join(dir, "artifacts"), time.Now)
	fallback := Settings{ListenHost: "0.0.0.0", Port: DefaultPort}
	if err := manager.Start(t.Context(), fallback); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Configure(t.Context(), fallback); err != nil {
		t.Fatal(err)
	}
	saved, exists, err := s.LoadAgentServiceSettings(t.Context())
	if err != nil || !exists || saved.Enabled || saved.Port != DefaultPort {
		t.Fatalf("saved settings=%+v exists=%v err=%v", saved, exists, err)
	}
}

func TestSavingUnchangedEnabledFallbackEstablishesPersistentPageSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(s, secret.New(s, v, time.Now), dir, filepath.Join(dir, "artifacts"), time.Now)
	manager.listen = func(_, _ string) (net.Listener, error) { return newBlockingListener(), nil }
	fallback := Settings{Enabled: true, ListenHost: "127.0.0.1", Port: 10443, AdvertisedHost: "control.lan", TLSNames: []string{"control.lan"}}
	if err := manager.Start(t.Context(), fallback); err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if _, err := manager.Configure(t.Context(), fallback); err != nil {
		t.Fatal(err)
	}
	saved, exists, err := s.LoadAgentServiceSettings(t.Context())
	if err != nil || !exists || !saved.Enabled || saved.AdvertisedHost != "control.lan" {
		t.Fatalf("saved settings=%+v exists=%v err=%v", saved, exists, err)
	}
}

func TestManagerStartsStopsAndRestoresPersistedListener(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(s, secret.New(s, v, time.Now), dir, filepath.Join(dir, "artifacts"), time.Now)
	var listened string
	listener := newBlockingListener()
	manager.listen = func(_, address string) (net.Listener, error) {
		listened = address
		return listener, nil
	}
	if err := manager.Start(t.Context(), Settings{Port: 9443, ListenHost: "0.0.0.0"}); err != nil {
		t.Fatal(err)
	}
	port := 10443
	status, err := manager.Configure(t.Context(), Settings{Enabled: true, ListenHost: "127.0.0.1", Port: port, AdvertisedHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.ServiceURL != "https://127.0.0.1:10443" || listened != "127.0.0.1:10443" {
		t.Fatalf("status=%+v", status)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted := New(s, secret.New(s, v, time.Now), dir, filepath.Join(dir, "artifacts"), time.Now)
	restartedListener := newBlockingListener()
	restarted.listen = func(_, address string) (net.Listener, error) {
		listened = address
		return restartedListener, nil
	}
	if err := restarted.Start(t.Context(), Settings{ListenHost: "0.0.0.0", Port: 9443}); err != nil {
		t.Fatal(err)
	}
	if status := restarted.Status(); !status.Running || status.Port != 10443 || listened != "127.0.0.1:10443" {
		t.Fatalf("restored status=%+v address=%s", status, listened)
	}

	if _, err := restarted.Configure(t.Context(), Settings{Enabled: false, ListenHost: "127.0.0.1", Port: port, AdvertisedHost: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if restarted.Status().Running {
		t.Fatal("listener still running after disable")
	}
	if err := restarted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	saved, exists, err := s.LoadAgentServiceSettings(t.Context())
	if err != nil || !exists || saved.Enabled {
		t.Fatalf("saved settings=%+v exists=%v err=%v", saved, exists, err)
	}
}

func TestManagerKeepsPreviousConfigurationWhenNewPortCannotStart(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "restic-control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := vault.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(s, secret.New(s, v, time.Now), dir, filepath.Join(dir, "artifacts"), time.Now)
	listeners := []*blockingListener{newBlockingListener(), newBlockingListener()}
	calls := 0
	manager.listen = func(_, address string) (net.Listener, error) {
		calls++
		if address == "127.0.0.1:11443" {
			return nil, errors.New("address already in use")
		}
		listener := listeners[0]
		listeners = listeners[1:]
		return listener, nil
	}
	if err := manager.Start(t.Context(), Settings{ListenHost: "0.0.0.0", Port: 9443}); err != nil {
		t.Fatal(err)
	}
	old := Settings{Enabled: true, ListenHost: "127.0.0.1", Port: 10443, AdvertisedHost: "127.0.0.1"}
	if _, err := manager.Configure(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Configure(t.Context(), Settings{Enabled: true, ListenHost: "127.0.0.1", Port: 11443, AdvertisedHost: "127.0.0.1"}); err == nil {
		t.Fatal("occupied port was accepted")
	}
	status := manager.Status()
	if !status.Running || status.Port != 10443 || calls != 3 {
		t.Fatalf("rollback status=%+v calls=%d", status, calls)
	}
	saved, exists, err := s.LoadAgentServiceSettings(t.Context())
	if err != nil || !exists || saved.Port != 10443 {
		t.Fatalf("saved settings=%+v exists=%v err=%v", saved, exists, err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type blockingListener struct {
	closed chan struct{}
}

func newBlockingListener() *blockingListener {
	return &blockingListener{closed: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (*blockingListener) Addr() net.Addr {
	return testAddr("127.0.0.1:10443")
}

type testAddr string

func (testAddr) Network() string  { return "tcp" }
func (a testAddr) String() string { return string(a) }
