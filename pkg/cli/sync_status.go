// corten-matrix - host-side management CLI.
//
// `corten-matrix sync-status` (and `sync-status 1` for the second account)
// prints the same backfill report as the management-room `sync-status`
// command, but without needing the daemon: it reads the account's config.yaml,
// opens the database it names, and runs read-only queries. That matters
// because the state people want to inspect — "did my history actually finish
// importing?" — is most interesting when the bridge is wedged, stopped, or has
// not been added to a Matrix client yet.
//
// The query/report implementation lives in pkg/syncstatus, a small package
// shared by the CGO-free CLI and the running connector command, so the two
// entry points cannot drift apart.

package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/util/dbutil"
	"gopkg.in/yaml.v3"

	"github.com/lrhodin/corten-matrix/pkg/syncstatus"
)

// syncStatusConfig is the slice of config.yaml this report needs. It is a
// hand-rolled struct rather than bridgeconfig.Config because that pulls in the
// framework's whole validation path, which would refuse to load a config the
// bridge itself is happily running on (missing registration file, unreachable
// homeserver) and turn a diagnostic into another thing that is broken.
type syncStatusConfig struct {
	Database struct {
		Type string `yaml:"type"`
		URI  string `yaml:"uri"`
	} `yaml:"database"`

	// Backfill.MaxInitialMessages is the TOP-LEVEL backfill key. The generated
	// config carries a second key with the same name under backfill.threads,
	// and the two routinely differ — the installer writes 2147483647 (the
	// uncapped sentinel) at the top level when the user declines a cap, while
	// the threads key keeps its default of 50. Reading the wrong one produces
	// a deliverable count that is wildly low and a percentage that still looks
	// plausible, so the nesting here is load-bearing. Unknown keys, including
	// threads, are ignored by yaml.v3.
	Backfill struct {
		MaxInitialMessages int `yaml:"max_initial_messages"`
	} `yaml:"backfill"`

	Network struct {
		CloudKitBackfill    bool `yaml:"cloudkit_backfill"`
		BridgeFilteredChats bool `yaml:"bridge_filtered_chats"`
	} `yaml:"network"`
}

func parseSyncStatusConfig(data []byte) (syncStatusConfig, error) {
	var cfg syncStatusConfig
	err := yaml.Unmarshal(data, &cfg)
	return cfg, err
}

// effectiveMaxInitialMessages mirrors the override the connector applies at
// startup (see PostInit): with CloudKit backfill on, anything below 100 is
// treated as "the user did not really mean to cap this" and forced to
// math.MaxInt32. Without that mirroring, a config left at the mautrix default
// of 50 would make the CLI report a 50-message-per-chat cap that the running
// bridge does not apply.
func (c syncStatusConfig) effectiveMaxInitialMessages() int {
	if c.Network.CloudKitBackfill && c.Backfill.MaxInitialMessages < 100 {
		return math.MaxInt32
	}
	return c.Backfill.MaxInitialMessages
}

// validateSyncStatusArgs accepts only the optional account selector. Keeping
// this separate from runSyncStatus makes the no-silent-fallback contract
// testable without invoking die (which exits the process).
func validateSyncStatusArgs(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: corten-matrix sync-status [1]")
	}
	if len(args) == 1 && args[0] != "1" {
		return fmt.Errorf("unknown sync-status argument %q (expected 1)", args[0])
	}
	return nil
}

func isSQLiteSyncStatusType(dbType string) bool {
	dbType = strings.ToLower(strings.TrimSpace(dbType))
	return strings.HasPrefix(dbType, "sqlite") || strings.HasPrefix(dbType, "litestream")
}

// readOnlySQLiteURI adds SQLite's VFS read-only mode without disturbing any
// bridge-specific query parameters. mode=ro is important beyond the absence
// of writes in our report: database/sql's SQLite driver otherwise creates a
// missing file as soon as the first query runs, turning a diagnostic typo into
// a new empty bridge database.
func readOnlySQLiteURI(uri string) (string, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return "", fmt.Errorf("empty SQLite database URI")
	}
	base, rawQuery, _ := strings.Cut(uri, "?")
	if base == ":memory:" || strings.HasPrefix(strings.ToLower(base), "file::memory:") {
		return "", fmt.Errorf("sync-status requires an existing file-backed SQLite database")
	}
	values := url.Values{}
	if rawQuery != "" {
		parsed, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid SQLite database URI: %w", err)
		}
		values = parsed
	}
	if strings.EqualFold(values.Get("mode"), "memory") {
		return "", fmt.Errorf("sync-status requires an existing file-backed SQLite database")
	}
	values.Set("mode", "ro")
	return base + "?" + values.Encode(), nil
}

// openSyncStatusDatabase deliberately does not use dbutil.NewWithDialect for
// SQLite: that helper calls sql.Open with the caller's original DSN, whose
// default mode is read-write/create. The daemon's sqlite3-fk-wal and litestream
// drivers also run a ConnectHook that sets journal_mode=WAL, which is a write
// and therefore fails against a read-only URI. The plain sqlite3 driver is the
// same SQLite implementation without that write-oriented hook, so use it for
// this read-only diagnostic while retaining the configured type for dialect
// translation. PostgreSQL remains on the normal path so external deployments
// keep working unchanged.
func openSyncStatusDatabase(cfg syncStatusConfig) (*dbutil.Database, error) {
	uri := cfg.Database.URI
	driverName := cfg.Database.Type
	if isSQLiteSyncStatusType(cfg.Database.Type) {
		var err error
		uri, err = readOnlySQLiteURI(uri)
		if err != nil {
			return nil, err
		}
		driverName = "sqlite3"
	}
	raw, err := sql.Open(driverName, uri)
	if err != nil {
		return nil, err
	}
	db, err := dbutil.NewWithDB(raw, cfg.Database.Type)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	// The daemon itself clamps SQLite to one connection. Match that here so a
	// report never competes with the live bridge for a writer slot, while still
	// retaining PostgreSQL's normal pool behavior.
	if isSQLiteSyncStatusType(cfg.Database.Type) {
		db.RawDB.SetMaxOpenConns(1)
	}
	return db, nil
}

// syncStatusDatabaseErrorClass deliberately discards driver text entirely.
// Matching and returning a sanitized copy of a DSN is not sufficient: drivers
// may decode credentials, normalize paths, or embed the secret in a nested
// error. Only broad operational classes are safe to print from this CLI.
func syncStatusDatabaseErrorClass(err error) string {
	switch {
	case err == nil:
		return "database operation failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "database query timed out"
	case errors.Is(err, os.ErrNotExist):
		return "database file not found"
	case errors.Is(err, os.ErrPermission):
		return "database permission denied"
	default:
		return "database operation failed"
	}
}

// runSyncStatus implements `corten-matrix sync-status [1]`.
func runSyncStatus(args []string) {
	if err := validateSyncStatusArgs(args); err != nil {
		die("%v", err)
	}
	dir := cortenDataDir()
	who := ""
	if len(args) > 0 && args[0] == "1" {
		dir = secondDataDir()
		who = " (second account)"
	}
	configPath := filepath.Join(dir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		die("Could not read %s: %v", configPath, err)
	}
	cfg, err := parseSyncStatusConfig(data)
	if err != nil {
		die("Could not parse %s: %v", configPath, err)
	}
	if cfg.Database.Type == "" || cfg.Database.URI == "" {
		die("No database configured in %s", configPath)
	}

	db, err := openSyncStatusDatabase(cfg)
	if err != nil {
		die("Could not open the database: %s", syncStatusDatabaseErrorClass(err))
	}
	defer db.Close()

	report, err := syncstatus.GetSyncStatus(context.Background(), db, syncstatus.SyncStatusOptions{
		// BridgeID and LoginID are left empty: only the database knows them,
		// and GetSyncStatus reads them from the single user_login row.
		MaxInitialMessages:  cfg.effectiveMaxInitialMessages(),
		BridgeFilteredChats: cfg.Network.BridgeFilteredChats,
		// SyncRunning and BatchSending stay nil — both are properties of the
		// running process, and inventing a value here would be a guess the
		// report then states as fact.
	})
	if err != nil {
		die("Could not read sync status from %s: %s", configPath,
			syncStatusDatabaseErrorClass(err))
	}

	fmt.Printf("\n  %scorten-matrix sync-status%s%s\n  %s%s%s\n\n",
		cBold+cAccent, cReset, who, cDim, configPath, cReset)
	fmt.Println(report.Format())
}
