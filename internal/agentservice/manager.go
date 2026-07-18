package agentservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maboo-run/shadoc/internal/agentassignment"
	"github.com/maboo-run/shadoc/internal/agentcontrol"
	"github.com/maboo-run/shadoc/internal/agentdeploy"
	"github.com/maboo-run/shadoc/internal/secret"
	"github.com/maboo-run/shadoc/internal/store"
)

const DefaultPort = 9443

var ErrDisabled = errors.New("Agent service is disabled")

type Settings struct {
	Enabled        bool
	ListenHost     string
	Port           int
	AdvertisedHost string
	TLSNames       []string
}

type Status struct {
	Enabled        bool   `json:"enabled"`
	Running        bool   `json:"running"`
	Port           int    `json:"port"`
	AdvertisedHost string `json:"advertisedHost"`
	ListenAddress  string `json:"listenAddress"`
	ServiceURL     string `json:"serviceUrl"`
	Error          string `json:"error,omitempty"`
}

type instance struct {
	server   *http.Server
	listener net.Listener
	control  *agentcontrol.Service
	deployer *agentdeploy.Service
}

type Manager struct {
	store       *store.Store
	secrets     *secret.Manager
	dataDir     string
	artifactDir string
	now         func() time.Time
	listen      func(string, string) (net.Listener, error)
	remover     *agentdeploy.RemovalService
	upgrader    *agentdeploy.UpgradeService

	configureMu sync.Mutex
	mu          sync.RWMutex
	settings    Settings
	instance    *instance
	lastError   string
}

func New(storage *store.Store, secrets *secret.Manager, dataDir, artifactDir string, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store: storage, secrets: secrets, dataDir: dataDir, artifactDir: artifactDir, now: now, listen: net.Listen,
		remover:  agentdeploy.NewRemovalService(storage, secrets, agentdeploy.SSHRemovalDialer{}, now),
		upgrader: agentdeploy.NewUpgradeService(storage, secrets, agentdeploy.ArtifactResolver{Dir: artifactDir}, agentdeploy.SSHUpgradeDialer{}, now),
	}
}

func Validate(settings Settings) error {
	settings = normalize(settings)
	if settings.Port < 1024 || settings.Port > 65535 {
		return errors.New("监听端口必须在 1024 到 65535 之间")
	}
	if !settings.Enabled {
		return nil
	}
	if settings.ListenHost == "" {
		return errors.New("监听地址不能为空")
	}
	host := settings.AdvertisedHost
	if host == "" {
		return errors.New("控制服务访问地址不能为空")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return errors.New("控制服务访问地址不能填写 0.0.0.0 或 ::")
		}
		return nil
	}
	if strings.ContainsAny(host, "\x00\r\n\t /\\:@?#[]") {
		return errors.New("控制服务访问地址必须是 IP 或域名，不能包含协议、端口或路径")
	}
	if !validDNSName(host) {
		return errors.New("控制服务访问地址不是有效的 DNS 域名")
	}
	parsed, err := url.Parse("https://" + net.JoinHostPort(host, strconv.Itoa(settings.Port)))
	if err != nil || parsed.Hostname() != host {
		return errors.New("控制服务访问地址必须是 IP 或域名")
	}
	return nil
}

func validDNSName(name string) bool {
	if len(name) == 0 || len(name) > 253 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !asciiAlphaNumeric(label[0]) || !asciiAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			if !asciiAlphaNumeric(label[i]) && label[i] != '-' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func normalize(settings Settings) Settings {
	settings.ListenHost = strings.TrimSpace(settings.ListenHost)
	settings.AdvertisedHost = strings.Trim(strings.TrimSpace(settings.AdvertisedHost), "[]")
	if settings.ListenHost == "" {
		settings.ListenHost = "0.0.0.0"
	}
	if settings.Port == 0 {
		settings.Port = DefaultPort
	}
	if settings.Enabled && len(settings.TLSNames) == 0 && settings.AdvertisedHost != "" {
		settings.TLSNames = []string{settings.AdvertisedHost}
	}
	return settings
}

func (m *Manager) Start(ctx context.Context, fallback Settings) error {
	m.configureMu.Lock()
	defer m.configureMu.Unlock()
	settings, exists, err := m.store.LoadAgentServiceSettings(ctx)
	if err != nil {
		return err
	}
	selected := normalize(fallback)
	if exists {
		selected = normalize(fromStored(settings))
	}
	if err := Validate(selected); err != nil {
		m.setUnavailable(selected, "Agent HTTPS 配置无效")
		return err
	}
	if !selected.Enabled {
		m.setUnavailable(selected, "")
		return nil
	}
	created, err := m.build(selected)
	if err != nil {
		m.setUnavailable(selected, "Agent HTTPS 监听启动失败")
		return err
	}
	m.install(selected, created)
	return nil
}

func (m *Manager) Configure(ctx context.Context, requested Settings) (Status, error) {
	m.configureMu.Lock()
	defer m.configureMu.Unlock()
	requested = normalize(requested)
	if err := Validate(requested); err != nil {
		return m.Status(), err
	}

	m.mu.RLock()
	oldSettings, oldInstance := m.settings, m.instance
	m.mu.RUnlock()
	if reflect.DeepEqual(oldSettings, requested) && (requested.Enabled == (oldInstance != nil)) {
		if err := m.store.SaveAgentServiceSettings(ctx, toStored(requested), m.now().UTC()); err != nil {
			return m.Status(), err
		}
		return m.Status(), nil
	}
	m.detach(oldInstance, requested)
	if err := stop(ctx, oldInstance); err != nil {
		if oldInstance != nil {
			_ = oldInstance.server.Close()
		}
		m.rollback(oldSettings)
		return m.Status(), err
	}

	var created *instance
	var err error
	if requested.Enabled {
		created, err = m.build(requested)
		if err != nil {
			m.rollback(oldSettings)
			return m.Status(), fmt.Errorf("start Agent HTTPS listener: %w", err)
		}
		m.install(requested, created)
	} else {
		m.setUnavailable(requested, "")
	}
	if err := m.store.SaveAgentServiceSettings(ctx, toStored(requested), m.now().UTC()); err != nil {
		m.detach(created, oldSettings)
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = stop(rollbackCtx, created)
		cancel()
		m.rollback(oldSettings)
		return m.Status(), err
	}
	return m.Status(), nil
}

func (m *Manager) rollback(settings Settings) {
	if !settings.Enabled {
		m.setUnavailable(settings, "")
		return
	}
	created, err := m.build(settings)
	if err != nil {
		m.setUnavailable(settings, "Agent HTTPS 监听回滚失败")
		slog.Error("restore Agent HTTPS listener", "error", err)
		return
	}
	m.install(settings, created)
}

func (m *Manager) build(settings Settings) (*instance, error) {
	address := net.JoinHostPort(settings.ListenHost, strconv.Itoa(settings.Port))
	listener, err := m.listen("tcp", address)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = listener.Close()
		}
	}()
	authority, err := agentcontrol.LoadOrCreateAuthority(filepath.Join(m.dataDir, "agent-pki"), m.now)
	if err != nil {
		return nil, err
	}
	certificate, err := agentcontrol.LoadOrCreateServerCertificate(filepath.Join(m.dataDir, "agent-pki"), authority, address, settings.TLSNames, m.now)
	if err != nil {
		return nil, err
	}
	control := agentcontrol.NewWithStore(authority, m.store, m.now)
	control.SetAssignmentHydrator(agentassignment.New(m.store, m.secrets).Build)
	deployer := agentdeploy.NewService(m.store, m.secrets, control, agentdeploy.ArtifactResolver{Dir: m.artifactDir}, agentdeploy.SSHDialer{}, m.now)
	server := &http.Server{
		Addr: address, Handler: agentcontrol.NewHandler(control),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		TLSConfig: agentcontrol.ServerTLSConfig(authority, certificate),
	}
	failed = false
	return &instance{server: server, listener: listener, control: control, deployer: deployer}, nil
}

func (m *Manager) install(settings Settings, active *instance) {
	m.mu.Lock()
	m.settings, m.instance, m.lastError = settings, active, ""
	m.mu.Unlock()
	slog.Info("Shadoc Agent HTTPS service listening", "address", active.server.Addr)
	go func() {
		err := active.server.ServeTLS(active.listener, "", "")
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		slog.Error("Agent HTTPS listener stopped", "error", err)
		m.mu.Lock()
		if m.instance == active {
			m.instance = nil
			m.lastError = "Agent HTTPS 监听异常停止"
		}
		m.mu.Unlock()
	}()
}

func (m *Manager) detach(active *instance, next Settings) {
	m.mu.Lock()
	if m.instance == active {
		m.instance = nil
	}
	m.settings, m.lastError = next, ""
	m.mu.Unlock()
}

func (m *Manager) setUnavailable(settings Settings, message string) {
	m.mu.Lock()
	m.settings, m.instance, m.lastError = settings, nil, message
	m.mu.Unlock()
}

func stop(ctx context.Context, active *instance) error {
	if active == nil {
		return nil
	}
	return active.server.Shutdown(ctx)
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	settings, running, lastError := m.settings, m.instance != nil, m.lastError
	m.mu.RUnlock()
	status := Status{
		Enabled: settings.Enabled, Running: running, Port: settings.Port,
		AdvertisedHost: settings.AdvertisedHost, Error: lastError,
	}
	if status.Port == 0 {
		status.Port = DefaultPort
	}
	status.ListenAddress = net.JoinHostPort(settings.ListenHost, strconv.Itoa(status.Port))
	if settings.AdvertisedHost != "" {
		status.ServiceURL = "https://" + net.JoinHostPort(settings.AdvertisedHost, strconv.Itoa(status.Port))
	}
	return status
}

func (m *Manager) CreateEnrollmentToken(ctx context.Context, lifetime time.Duration) (string, string, error) {
	m.mu.RLock()
	active := m.instance
	m.mu.RUnlock()
	if active == nil {
		return "", "", ErrDisabled
	}
	token, err := active.control.CreateEnrollmentToken(ctx, lifetime)
	return token, active.control.CAPEM(), err
}

func (m *Manager) Deploy(ctx context.Context, request agentdeploy.DeployRequest, report agentdeploy.StageReporter) (agentdeploy.DeployResult, error) {
	m.mu.RLock()
	active := m.instance
	m.mu.RUnlock()
	if active == nil {
		return agentdeploy.DeployResult{}, ErrDisabled
	}
	return active.deployer.Deploy(ctx, request, report)
}

func (m *Manager) Uninstall(ctx context.Context, agentID string, report agentdeploy.StageReporter) (agentdeploy.RemovalResult, error) {
	if m == nil || m.remover == nil {
		return agentdeploy.RemovalResult{}, errors.New("Agent remover is unavailable")
	}
	return m.remover.Uninstall(ctx, agentID, report)
}

func (m *Manager) Upgrade(ctx context.Context, request agentdeploy.UpgradeRequest, report agentdeploy.StageReporter) (agentdeploy.UpgradeResult, error) {
	if m == nil || m.upgrader == nil {
		return agentdeploy.UpgradeResult{}, errors.New("Agent upgrader is unavailable")
	}
	return m.upgrader.Upgrade(ctx, request, report)
}

func (m *Manager) ReprobeTools(ctx context.Context, agentID string, report agentdeploy.StageReporter) (agentdeploy.ToolProbeResult, error) {
	if m == nil || m.upgrader == nil {
		return agentdeploy.ToolProbeResult{}, errors.New("Agent tool prober is unavailable")
	}
	return m.upgrader.ReprobeTools(ctx, agentID, report)
}

func (m *Manager) Close(ctx context.Context) error {
	m.configureMu.Lock()
	defer m.configureMu.Unlock()
	m.mu.Lock()
	active := m.instance
	m.instance = nil
	m.mu.Unlock()
	return stop(ctx, active)
}

func fromStored(settings store.AgentServiceSettings) Settings {
	return Settings{
		Enabled: settings.Enabled, ListenHost: settings.ListenHost, Port: settings.Port,
		AdvertisedHost: settings.AdvertisedHost, TLSNames: append([]string(nil), settings.TLSNames...),
	}
}

func toStored(settings Settings) store.AgentServiceSettings {
	return store.AgentServiceSettings{
		Enabled: settings.Enabled, ListenHost: settings.ListenHost, Port: settings.Port,
		AdvertisedHost: settings.AdvertisedHost, TLSNames: append([]string(nil), settings.TLSNames...),
	}
}
