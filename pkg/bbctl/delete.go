package bbctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	"maunium.net/go/mautrix/appservice"

	"github.com/beeper/bridge-manager/api/beeperapi"
)

const (
	deleteVerificationAttempts = 10
	deleteVerificationInterval = 3 * time.Second
)

type appServiceDeleteClient interface {
	DeleteAppService(context.Context, string) error
	GetAppService(context.Context, string) (appservice.Registration, error)
}

type bridgeDeleteDependencies struct {
	appservices  appServiceDeleteClient
	deleteBridge func(domain, bridgeName, token string) error
	whoami       func(domain, token string) (*beeperapi.RespWhoami, error)
	wait         func(context.Context, time.Duration) error
}

var deleteCommand = &cli.Command{
	Name:      "delete",
	Usage:     "Delete a bridge registration",
	ArgsUsage: "BRIDGE",
	Before:    requiresAuth,
	Action:    cmdDelete,
}

func cmdDelete(ctx *cli.Context) error {
	if ctx.NArg() == 0 {
		return fmt.Errorf("you must specify a bridge name")
	}
	bridge := ctx.Args().Get(0)

	envCfg := getEnvConfig(ctx)
	hungryClient := getHungryClient(ctx)

	err := deleteBridgeAndVerify(ctx.Context, bridge, envCfg.AccessToken, bridgeDeleteDependencies{
		appservices:  hungryClient,
		deleteBridge: beeperapi.DeleteBridge,
		whoami:       beeperapi.Whoami,
		wait:         waitForDeleteVerification,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Bridge '%s' deleted\n", bridge)
	return nil
}

// deleteBridgeAndVerify removes both server-side pieces of a Beeper bridge.
// Each delete accepts an explicit not-found response so callers can safely retry
// after one endpoint succeeded and the other failed. Success is reported only
// after both APIs independently confirm that the bridge is absent.
func deleteBridgeAndVerify(ctx context.Context, bridge, accessToken string, deps bridgeDeleteDependencies) error {
	if deps.appservices == nil || deps.deleteBridge == nil || deps.whoami == nil || deps.wait == nil {
		return errors.New("bridge deletion is not fully configured")
	}

	if err := deps.appservices.DeleteAppService(ctx, bridge); err != nil && !isHTTPNotFound(err) {
		return fmt.Errorf("failed to delete appservice: %w", err)
	}
	if err := deps.deleteBridge(baseDomain, bridge, accessToken); err != nil && !isBeeperNotFoundError(err) {
		return fmt.Errorf("failed to delete bridge from Beeper API: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < deleteVerificationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("bridge deletion verification cancelled: %w", err)
		}

		appserviceAbsent, err := isAppServiceAbsent(ctx, bridge, deps.appservices)
		if err != nil {
			return fmt.Errorf("failed to verify appservice deletion: %w", err)
		}
		bridgeAbsent, err := isBridgeAbsent(bridge, accessToken, deps.whoami)
		if err != nil {
			return fmt.Errorf("failed to verify Beeper bridge deletion: %w", err)
		}
		if appserviceAbsent && bridgeAbsent {
			return nil
		}

		lastErr = fmt.Errorf("server still reports appservice present=%t, bridge present=%t", !appserviceAbsent, !bridgeAbsent)
		if attempt+1 < deleteVerificationAttempts {
			if err = deps.wait(ctx, deleteVerificationInterval); err != nil {
				return fmt.Errorf("bridge deletion verification interrupted: %w", err)
			}
		}
	}
	return fmt.Errorf("bridge deletion did not converge after %d verification attempts: %w", deleteVerificationAttempts, lastErr)
}

func isAppServiceAbsent(ctx context.Context, bridge string, client appServiceDeleteClient) (bool, error) {
	_, err := client.GetAppService(ctx, bridge)
	if err == nil {
		return false, nil
	}
	if isHTTPNotFound(err) {
		return true, nil
	}
	return false, err
}

func isBridgeAbsent(bridge, accessToken string, whoami func(string, string) (*beeperapi.RespWhoami, error)) (bool, error) {
	resp, err := whoami(baseDomain, accessToken)
	if err != nil {
		return false, err
	}
	if resp == nil {
		return false, errors.New("Beeper API returned an empty whoami response")
	}
	if resp.User.Bridges == nil {
		return false, errors.New("Beeper API returned whoami without a bridges map")
	}
	_, present := resp.User.Bridges[bridge]
	return !present, nil
}

func waitForDeleteVerification(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isHTTPNotFound(err error) bool {
	if err == nil {
		return false
	}
	var statusError interface{ IsStatus(int) bool }
	return errors.As(err, &statusError) && statusError.IsStatus(http.StatusNotFound)
}

func isBeeperNotFoundError(err error) bool {
	if isHTTPNotFound(err) {
		return true
	}
	// beeperapi currently returns formatted errors rather than a typed HTTP
	// error. Restrict the fallback to the exact status formats it emits.
	message := strings.ToLower(err.Error())
	return strings.HasPrefix(message, "server returned error (http 404):") ||
		message == "unexpected status code 404"
}
