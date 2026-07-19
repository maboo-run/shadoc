package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/maboo-run/shadoc/internal/domain"
)

const nativeProbeTimeout = 10 * time.Second

type nativeDB interface {
	PingContext(context.Context) error
	QueryContext(context.Context, string, ...any) (nativeRows, error)
	Close() error
}

type nativeRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type sqlNativeDB struct {
	*sql.DB
}

func (db sqlNativeDB) QueryContext(ctx context.Context, query string, args ...any) (nativeRows, error) {
	rows, err := db.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlNativeRows{Rows: rows}, nil
}

type sqlNativeRows struct {
	*sql.Rows
}

// NativeTester verifies the database endpoint through the database wire
// protocol. It intentionally does not invoke mysqldump, psql, or any other
// external client. The external clients are checked separately by
// SystemVerifier and remain responsible for actual backup and restore work.
type NativeTester struct {
	Now  func() time.Time
	Open func(context.Context, domain.DatabaseConnection, string) (nativeDB, func(), error)
}

func (t NativeTester) Test(ctx context.Context, connection domain.DatabaseConnection, password string) Verification {
	now := t.Now
	if now == nil {
		now = time.Now
	}
	result := Verification{CheckedAt: now().UTC()}
	fail := func() Verification {
		result.Error = "数据库网络、TLS、认证或权限预检失败"
		return result
	}
	if connection.Validate() != nil || password == "" || !validNativeTLS(connection) {
		result.Error = "数据库连接配置或凭据无效"
		return result
	}
	probeCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, nativeProbeTimeout)
		defer cancel()
	}
	open := t.Open
	if open == nil {
		open = openNativeDatabase
	}
	db, cleanup, err := open(probeCtx, connection, password)
	if err != nil || db == nil {
		if cleanup != nil {
			cleanup()
		}
		return fail()
	}
	if cleanup != nil {
		defer cleanup()
	}
	if err := db.PingContext(probeCtx); err != nil {
		return fail()
	}
	switch connection.Engine {
	case domain.MySQL:
		return t.verifyMySQL(probeCtx, db, connection, result)
	case domain.PostgreSQL:
		return t.verifyPostgreSQL(probeCtx, db, connection, result)
	default:
		return fail()
	}
}

func (t NativeTester) verifyMySQL(ctx context.Context, db nativeDB, connection domain.DatabaseConnection, result Verification) Verification {
	var version string
	if err := queryOne(ctx, db, "SELECT VERSION()", nil, &version); err != nil {
		result.Error = "无法读取数据库服务端版本"
		return result
	}
	rows, err := db.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		result.Error = "数据库账号权限检查失败"
		return result
	}
	var grants []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			_ = rows.Close()
			result.Error = "数据库账号权限检查失败"
			return result
		}
		grants = append(grants, strings.ToUpper(grant))
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		result.Error = "数据库账号缺少当前用途所需权限"
		return result
	}
	if connection.Purpose == domain.RestoreConnection {
		// A restore connection must be able to create or alter its target. Do
		// not require a specific grant layout because MySQL/MariaDB format it
		// differently across versions; ALL PRIVILEGES is also valid here.
		if !containsAnyGrant(grants, "CREATE", "ALL PRIVILEGES") {
			result.Error = "数据库账号缺少当前用途所需权限"
			return result
		}
	} else if !containsAnyGrant(grants, "SELECT", "ALL PRIVILEGES") {
		result.Error = "数据库账号缺少当前用途所需权限"
		return result
	}
	result.ServerVersion = strings.TrimSpace(version)
	if result.ServerVersion == "" {
		result.Error = "无法读取数据库服务端版本"
	}
	return result
}

func (t NativeTester) verifyPostgreSQL(ctx context.Context, db nativeDB, connection domain.DatabaseConnection, result Verification) Verification {
	privilege := "CONNECT"
	if connection.Purpose == domain.RestoreConnection {
		privilege = "CREATE"
	}
	var version string
	var allowed bool
	query := "SELECT current_setting('server_version'), has_database_privilege(current_user,current_database(), $1)"
	if err := queryOne(ctx, db, query, []any{privilege}, &version, &allowed); err != nil {
		result.Error = "数据库网络、TLS、认证或权限预检失败"
		return result
	}
	if !allowed {
		result.Error = "数据库账号缺少当前用途所需权限"
		return result
	}
	result.ServerVersion = strings.TrimSpace(version)
	if result.ServerVersion == "" {
		result.Error = "无法读取数据库服务端版本"
	}
	return result
}

func containsAnyGrant(grants []string, values ...string) bool {
	for _, grant := range grants {
		for _, value := range values {
			if strings.Contains(grant, value) {
				return true
			}
		}
	}
	return false
}

func queryOne(ctx context.Context, db nativeDB, query string, args []any, destinations ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(destinations...); err != nil {
		return err
	}
	return rows.Err()
}

func openNativeDatabase(ctx context.Context, connection domain.DatabaseConnection, password string) (nativeDB, func(), error) {
	switch connection.Engine {
	case domain.MySQL:
		return openNativeMySQL(ctx, connection, password)
	case domain.PostgreSQL:
		return openNativePostgreSQL(ctx, connection, password)
	default:
		return nil, func() {}, errors.New("unsupported database engine")
	}
}

var mysqlTLSSequence atomic.Uint64

func openNativeMySQL(_ context.Context, connection domain.DatabaseConnection, password string) (nativeDB, func(), error) {
	tlsName, deregister, err := registerMySQLTLS(connection)
	if err != nil {
		return nil, func() {}, err
	}
	cfg := mysqlDriver.NewConfig()
	cfg.User = connection.Username
	cfg.Passwd = password
	cfg.Timeout = nativeProbeTimeout
	cfg.ReadTimeout = nativeProbeTimeout
	cfg.WriteTimeout = nativeProbeTimeout
	if connection.Network == domain.UnixNetwork {
		cfg.Net = "unix"
		cfg.Addr = connection.SocketPath
	} else {
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(strings.Trim(connection.Host, "[]"), strconv.Itoa(connection.Port))
	}
	if tlsName != "" {
		cfg.TLSConfig = tlsName
		cfg.AllowFallbackToPlaintext = connection.TLS.Mode == "preferred" || connection.TLS.Mode == ""
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		deregister()
		return nil, func() {}, err
	}
	return sqlNativeDB{DB: db}, func() {
		_ = db.Close()
		deregister()
	}, nil
}

func openNativePostgreSQL(_ context.Context, connection domain.DatabaseConnection, password string) (nativeDB, func(), error) {
	mode, err := postgresTLSMode(connection.TLS.Mode)
	if err != nil {
		return nil, func() {}, err
	}
	host := connection.Host
	settings := []string{
		"host=" + quotePGSetting(host),
		"database=" + quotePGSetting(connection.Username),
		"user=''",
		"password=''",
		"sslmode=" + quotePGSetting(mode),
		"sslrootcert=" + quotePGSetting(connection.TLS.CA),
		"sslcert=" + quotePGSetting(connection.TLS.ClientCert),
		"sslkey=" + quotePGSetting(connection.TLS.ClientKey),
		"connect_timeout=" + strconv.Itoa(int(nativeProbeTimeout/time.Second)),
	}
	if connection.Network == domain.UnixNetwork {
		settings[0] = "host=" + quotePGSetting(connection.SocketPath)
	} else {
		settings = append(settings, "port="+strconv.Itoa(connection.Port))
	}
	config, err := pgx.ParseConfig(strings.Join(settings, " "))
	if err != nil {
		return nil, func() {}, err
	}
	config.User = connection.Username
	config.Password = password
	if connection.TLS.ServerName != "" {
		if config.TLSConfig != nil {
			config.TLSConfig.ServerName = connection.TLS.ServerName
		}
		for _, fallback := range config.Fallbacks {
			if fallback.TLSConfig != nil {
				fallback.TLSConfig.ServerName = connection.TLS.ServerName
			}
		}
	}
	db := stdlib.OpenDB(*config)
	return sqlNativeDB{DB: db}, func() { _ = db.Close() }, nil
}

func quotePGSetting(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.NewReplacer(`\\`, `\\\\`, `'`, `\\'`).Replace(value) + "'"
}

func postgresTLSMode(mode string) (string, error) {
	switch mode {
	case "", "preferred":
		return "prefer", nil
	case "required":
		return "require", nil
	case "verify-ca", "verify-full", "disabled":
		if mode == "disabled" {
			return "disable", nil
		}
		return mode, nil
	default:
		return "", errors.New("数据库 TLS 模式无效")
	}
}

func validNativeTLS(connection domain.DatabaseConnection) bool {
	mode := connection.TLS.Mode
	if mode != "" && mode != "preferred" && mode != "required" && mode != "verify-ca" && mode != "verify-full" && mode != "disabled" {
		return false
	}
	if (connection.TLS.ClientCert == "") != (connection.TLS.ClientKey == "") {
		return false
	}
	for _, path := range []string{connection.TLS.CA, connection.TLS.ClientCert, connection.TLS.ClientKey} {
		if path != "" && !filepath.IsAbs(path) {
			return false
		}
	}
	if mode == "verify-full" && connection.Network == domain.UnixNetwork && connection.TLS.ServerName == "" {
		return false
	}
	return true
}

func registerMySQLTLS(connection domain.DatabaseConnection) (string, func(), error) {
	mode := connection.TLS.Mode
	if mode == "" {
		mode = "preferred"
	}
	if mode == "disabled" {
		return "", func() {}, nil
	}
	rootCAs, err := loadCertPool(connection.TLS.CA)
	if err != nil {
		return "", func() {}, err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: rootCAs}
	if connection.TLS.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(connection.TLS.ClientCert, connection.TLS.ClientKey)
		if err != nil {
			return "", func() {}, err
		}
		config.Certificates = []tls.Certificate{cert}
	}
	if connection.Network == domain.TCPNetwork {
		config.ServerName = connection.TLS.ServerName
		if config.ServerName == "" {
			config.ServerName = strings.Trim(connection.Host, "[]")
		}
	}
	switch mode {
	case "preferred", "required":
		// MySQL's PREFERRED/REQUIRED modes encrypt the session without
		// requiring a trusted server certificate. PREFERRED may fall back to
		// plaintext; REQUIRED may not.
		config.InsecureSkipVerify = true //nolint:gosec -- certificate verification is controlled by the selected MySQL TLS mode.
	case "verify-ca":
		config.InsecureSkipVerify = true //nolint:gosec -- VerifyConnection performs chain-only validation below.
		config.VerifyConnection = verifyCertificateChain(config.RootCAs)
	case "verify-full":
		if config.RootCAs == nil {
			config.RootCAs, err = x509.SystemCertPool()
			if err != nil {
				return "", func() {}, err
			}
		}
	default:
		return "", func() {}, errors.New("数据库 TLS 模式无效")
	}
	name := fmt.Sprintf("shadoc-db-%d", mysqlTLSSequence.Add(1))
	if err := mysqlDriver.RegisterTLSConfig(name, config); err != nil {
		return "", func() {}, err
	}
	return name, func() { mysqlDriver.DeregisterTLSConfig(name) }, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(content) {
		return nil, errors.New("无法读取 TLS CA 证书")
	}
	return pool, nil
}

func verifyCertificateChain(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("服务器未提供 TLS 证书")
		}
		intermediates := x509.NewCertPool()
		for _, certificate := range state.PeerCertificates[1:] {
			intermediates.AddCert(certificate)
		}
		_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		return err
	}
}
