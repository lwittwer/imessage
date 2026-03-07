package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v2"

	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/beeper/bridge-manager/api/beeperapi"
)

var stopCommand = &cli.Command{
	Name:      "stop",
	Usage:     "Tell Beeper that the bridge is stopped (not running)",
	ArgsUsage: "BRIDGE",
	Before:    requiresAuth,
	Action:    cmdStop,
}

func findBridgeConfig() (string, error) {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	path := filepath.Join(dataDir, "mautrix-imessage", "config.yaml")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("config not found at %s", path)
	}
	return path, nil
}

func readASToken(configPath string) (string, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "as_token:") {
			token := strings.TrimSpace(strings.TrimPrefix(line, "as_token:"))
			return token, nil
		}
	}
	return "", fmt.Errorf("as_token not found in config")
}

func cmdStop(ctx *cli.Context) error {
	if ctx.NArg() == 0 {
		return fmt.Errorf("you must specify a bridge name")
	}
	bridge := ctx.Args().Get(0)

	configPath, err := findBridgeConfig()
	if err != nil {
		return err
	}
	asToken, err := readASToken(configPath)
	if err != nil {
		return fmt.Errorf("failed to read AS token from %s: %w", configPath, err)
	}

	envCfg := getEnvConfig(ctx)
	err = beeperapi.PostBridgeState(baseDomain, envCfg.Username, bridge, asToken, beeperapi.ReqPostBridgeState{
		StateEvent:   status.StateBridgeUnreachable,
		Reason:       "SELF_HOST_STOPPED",
		IsSelfHosted: true,
		BridgeType:   "imessage",
	})
	if err != nil {
		return fmt.Errorf("failed to post stopped state: %w", err)
	}
	fmt.Printf("Bridge '%s' stopped\n", bridge)
	return nil
}
