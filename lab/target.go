package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type target struct{ host, port, user, pass, db, secretVersion string }

func targetFromEnv() target {
	t := target{
		host: os.Getenv("DB_HOST"), port: os.Getenv("DB_PORT"),
		user: os.Getenv("DB_USER"), pass: os.Getenv("DB_PASS"), db: os.Getenv("DB_NAME"),
	}
	if t.port == "" {
		t.port = "3306"
	}
	if t.host == "" || t.user == "" {
		log.Fatal("DB_HOST / DB_USER / DB_PASS / DB_NAME must be set (use run.sh)")
	}
	return t
}

// refreshCreds re-reads username/password from Secrets Manager (via the aws CLI) so that new pools
// pick up rotated credentials, like an app that fetches the secret at (re)start.
func refreshCreds(t *target, arn string) error {
	out, err := exec.Command("aws", "secretsmanager", "get-secret-value", "--secret-id", arn,
		"--query", "{version:VersionId,secret:SecretString}", "--output", "json").Output()
	if err != nil {
		return err
	}
	var result struct{ Version, Secret string }
	if err := json.Unmarshal(out, &result); err != nil {
		return err
	}
	var v struct{ Username, Password string }
	if err := json.Unmarshal([]byte(result.Secret), &v); err != nil {
		return err
	}
	if result.Version == "" || v.Username == "" || v.Password == "" {
		return errors.New("incomplete AWSCURRENT credential")
	}
	t.user, t.pass, t.secretVersion = v.Username, v.Password, result.Version
	return nil
}

// triggerRotation asks Secrets Manager to rotate now (single-user password rotation).
func triggerRotation(arn string) {
	out, err := exec.Command("aws", "secretsmanager", "rotate-secret", "--secret-id", arn).CombinedOutput()
	if err != nil {
		log.Printf("ROTATE-SECRET failed: %v: %s", err, out)
		return
	}
	log.Printf("ROTATE-SECRET requested at %s", time.Now().UTC().Format(time.RFC3339))
}

// effectiveTLS resolves a --tls setting. "none" is plain TCP on purpose. "" follows the infrastructure: when the Proxy requires TLS
// (REQUIRE_TLS=true in /etc/exp.env, from the stack's requireTls context) a plain connection would be refused, so the lab uses
// "skip-verify" (encrypted, but the server certificate is NOT verified: a different condition from verified TLS). Anything else is
// used as given.
func effectiveTLS(requested string) string {
	switch requested {
	case "none":
		return ""
	case "":
		if os.Getenv("REQUIRE_TLS") == "true" {
			return "skip-verify"
		}
		return ""
	}
	return requested
}

type poolOpts struct {
	maxOpen, maxIdle int
	lifetime, idle   time.Duration
	interpolate      bool
	tls              string // "" (follow the infrastructure) | "none" (plain TCP) | "skip-verify" (TLS, certificate not verified)
}

// registry of every *sql.DB ever created, so we can report leaked (never closed) pools.
var (
	regMu  sync.Mutex
	allDBs []*sql.DB
	closed = map[*sql.DB]bool{}
)

func newDB(t target, o poolOpts) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net, mc.DBName = t.user, t.pass, "tcp", t.db
	mc.Addr = t.host + ":" + t.port
	if tl := effectiveTLS(o.tls); tl != "" {
		mc.TLSConfig = tl
	}
	mc.InterpolateParams = o.interpolate // false => args use server-side prepared statements (pins on RDS Proxy)
	mc.Timeout, mc.ReadTimeout, mc.WriteTimeout = 10*time.Second, 30*time.Second, 30*time.Second
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(o.maxOpen)
	db.SetMaxIdleConns(o.maxIdle)
	db.SetConnMaxLifetime(o.lifetime) // 0 = unlimited
	db.SetConnMaxIdleTime(o.idle)     // 0 = unlimited
	return db, nil
}

func openPool(t target, o poolOpts) (*sql.DB, *gorm.DB, error) {
	db, err := newDB(t, o)
	if err != nil {
		return nil, nil, err
	}
	gdb, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: db, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	regMu.Lock()
	allDBs = append(allDBs, db)
	regMu.Unlock()
	return db, gdb, nil
}

func closePool(db *sql.DB) {
	_ = db.Close()
	regMu.Lock()
	closed[db] = true
	regMu.Unlock()
}
