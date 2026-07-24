package bbctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/beeper/bridge-manager/api/beeperapi"
	"maunium.net/go/mautrix/appservice"
)

type fakeAppServiceDeleteClient struct {
	deleteErr   error
	getErrs     []error
	deleteCalls int
	getCalls    int
}

func (f *fakeAppServiceDeleteClient) DeleteAppService(context.Context, string) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeAppServiceDeleteClient) GetAppService(context.Context, string) (appservice.Registration, error) {
	f.getCalls++
	if len(f.getErrs) == 0 {
		return appservice.Registration{}, nil
	}
	index := f.getCalls - 1
	if index >= len(f.getErrs) {
		index = len(f.getErrs) - 1
	}
	return appservice.Registration{}, f.getErrs[index]
}

type fakeHTTPStatusError struct {
	status int
}

func (f fakeHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", f.status)
}

func (f fakeHTTPStatusError) IsStatus(status int) bool {
	return f.status == status
}

func absentWhoami(string, string) (*beeperapi.RespWhoami, error) {
	return &beeperapi.RespWhoami{User: beeperapi.WhoamiUser{
		Bridges: map[string]beeperapi.WhoamiBridge{},
	}}, nil
}

func noWait(context.Context, time.Duration) error {
	return nil
}

func TestDeleteBridgeAndVerifySuccess(t *testing.T) {
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{fakeHTTPStatusError{status: http.StatusNotFound}}}
	deleteBridgeCalls := 0
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices: appservices,
		deleteBridge: func(domain, bridge, token string) error {
			deleteBridgeCalls++
			if domain != baseDomain || bridge != "sh-imessage" || token != "token" {
				t.Fatalf("unexpected delete arguments: %q %q %q", domain, bridge, token)
			}
			return nil
		},
		whoami: absentWhoami,
		wait:   noWait,
	})
	if err != nil {
		t.Fatalf("deleteBridgeAndVerify returned error: %v", err)
	}
	if appservices.deleteCalls != 1 || appservices.getCalls != 1 || deleteBridgeCalls != 1 {
		t.Fatalf("unexpected calls: delete appservice=%d, get appservice=%d, delete bridge=%d", appservices.deleteCalls, appservices.getCalls, deleteBridgeCalls)
	}
}

func TestDeleteBridgeAndVerifyIsIdempotentWhenAlreadyAbsent(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{
		deleteErr: notFound,
		getErrs:   []error{notFound},
	}
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices: appservices,
		deleteBridge: func(string, string, string) error {
			return errors.New("server returned error (HTTP 404): bridge not found")
		},
		whoami: absentWhoami,
		wait:   noWait,
	})
	if err != nil {
		t.Fatalf("already-absent deletion returned error: %v", err)
	}
}

func TestDeleteBridgeAndVerifyCanRetryAfterPartialSuccess(t *testing.T) {
	appservices := &fakeAppServiceDeleteClient{}
	firstErr := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices: appservices,
		deleteBridge: func(string, string, string) error {
			return errors.New("upstream unavailable")
		},
		whoami: absentWhoami,
		wait:   noWait,
	})
	if firstErr == nil {
		t.Fatal("partial deletion unexpectedly succeeded")
	}

	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices.deleteErr = notFound
	appservices.getErrs = []error{notFound}
	secondErr := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami:       absentWhoami,
		wait:         noWait,
	})
	if secondErr != nil {
		t.Fatalf("retry after partial deletion returned error: %v", secondErr)
	}
	if appservices.deleteCalls != 2 {
		t.Fatalf("appservice delete calls = %d, want 2", appservices.deleteCalls)
	}
}

func TestDeleteBridgeAndVerifyFailsOnAppServiceDeleteError(t *testing.T) {
	appservices := &fakeAppServiceDeleteClient{deleteErr: errors.New("permission denied")}
	bridgeDeleteCalled := false
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices: appservices,
		deleteBridge: func(string, string, string) error {
			bridgeDeleteCalled = true
			return nil
		},
		whoami: absentWhoami,
		wait:   noWait,
	})
	if err == nil || !strings.Contains(err.Error(), "failed to delete appservice") {
		t.Fatalf("expected appservice deletion error, got %v", err)
	}
	if bridgeDeleteCalled {
		t.Fatal("Beeper bridge delete was called after appservice delete failed")
	}
}

func TestDeleteBridgeAndVerifyFailsOnBeeperDeleteError(t *testing.T) {
	appservices := &fakeAppServiceDeleteClient{}
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices: appservices,
		deleteBridge: func(string, string, string) error {
			return errors.New("upstream unavailable")
		},
		whoami: absentWhoami,
		wait:   noWait,
	})
	if err == nil || !strings.Contains(err.Error(), "failed to delete bridge from Beeper API") {
		t.Fatalf("expected Beeper deletion error, got %v", err)
	}
	if appservices.getCalls != 0 {
		t.Fatalf("verification ran after Beeper deletion failed: %d calls", appservices.getCalls)
	}
}

func TestDeleteBridgeAndVerifyWaitsForPostcondition(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{nil, notFound}}
	whoamiCalls := 0
	waits := 0
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami: func(string, string) (*beeperapi.RespWhoami, error) {
			whoamiCalls++
			resp := &beeperapi.RespWhoami{User: beeperapi.WhoamiUser{
				Bridges: map[string]beeperapi.WhoamiBridge{},
			}}
			if whoamiCalls == 1 {
				resp.User.Bridges = map[string]beeperapi.WhoamiBridge{"sh-imessage": {}}
			}
			return resp, nil
		},
		wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("eventually converged deletion returned error: %v", err)
	}
	if appservices.getCalls != 2 || whoamiCalls != 2 || waits != 1 {
		t.Fatalf("unexpected verification calls: appservice=%d whoami=%d waits=%d", appservices.getCalls, whoamiCalls, waits)
	}
}

func TestDeleteBridgeAndVerifyFailsWhenPostconditionNeverConverges(t *testing.T) {
	appservices := &fakeAppServiceDeleteClient{}
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami: func(string, string) (*beeperapi.RespWhoami, error) {
			return &beeperapi.RespWhoami{User: beeperapi.WhoamiUser{
				Bridges: map[string]beeperapi.WhoamiBridge{"sh-imessage": {}},
			}}, nil
		},
		wait: noWait,
	})
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("expected convergence error, got %v", err)
	}
	if appservices.getCalls != deleteVerificationAttempts {
		t.Fatalf("got %d verification attempts, want %d", appservices.getCalls, deleteVerificationAttempts)
	}
}

func TestDeleteBridgeAndVerifyRetriesAppServiceVerificationError(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{
		errors.New("verification unavailable"),
		notFound,
	}}
	waits := 0
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami:       absentWhoami,
		wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("eventually confirmed deletion returned error: %v", err)
	}
	if appservices.getCalls != 2 || waits != 1 {
		t.Fatalf("unexpected verification calls: appservice=%d waits=%d", appservices.getCalls, waits)
	}
}

func TestDeleteBridgeAndVerifyRetriesWhoamiVerificationError(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{notFound}}
	whoamiCalls := 0
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami: func(string, string) (*beeperapi.RespWhoami, error) {
			whoamiCalls++
			if whoamiCalls == 1 {
				return nil, errors.New("whoami unavailable")
			}
			return absentWhoami("", "")
		},
		wait: noWait,
	})
	if err != nil {
		t.Fatalf("eventually confirmed deletion returned error: %v", err)
	}
	if appservices.getCalls != 2 || whoamiCalls != 2 {
		t.Fatalf("unexpected verification calls: appservice=%d whoami=%d", appservices.getCalls, whoamiCalls)
	}
}

func TestDeleteBridgeAndVerifyPreservesFinalVerificationError(t *testing.T) {
	verificationErr := errors.New("verification unavailable")
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{verificationErr}}
	err := deleteBridgeAndVerify(context.Background(), "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami:       absentWhoami,
		wait:         noWait,
	})
	if err == nil || !errors.Is(err, verificationErr) {
		t.Fatalf("expected final verification error to be preserved, got %v", err)
	}
	if appservices.getCalls != deleteVerificationAttempts {
		t.Fatalf("got %d verification attempts, want %d", appservices.getCalls, deleteVerificationAttempts)
	}
}

func TestDeleteBridgeAndVerifyStopsWhenContextIsCancelledDuringVerification(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{notFound}}
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	err := deleteBridgeAndVerify(ctx, "sh-imessage", "token", bridgeDeleteDependencies{
		appservices:  appservices,
		deleteBridge: func(string, string, string) error { return nil },
		whoami: func(string, string) (*beeperapi.RespWhoami, error) {
			cancel()
			return nil, errors.New("whoami interrupted")
		},
		wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if appservices.getCalls != 1 || waits != 0 {
		t.Fatalf("unexpected verification calls: appservice=%d waits=%d", appservices.getCalls, waits)
	}
}

func TestDeleteBridgeAndVerifyStopsWhenBlockedWhoamiIsCancelled(t *testing.T) {
	notFound := fakeHTTPStatusError{status: http.StatusNotFound}
	appservices := &fakeAppServiceDeleteClient{getErrs: []error{notFound}}
	whoamiStarted := make(chan struct{})
	releaseWhoami := make(chan struct{})
	defer close(releaseWhoami)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deleteDone := make(chan error, 1)

	go func() {
		deleteDone <- deleteBridgeAndVerify(ctx, "sh-imessage", "token", bridgeDeleteDependencies{
			appservices:  appservices,
			deleteBridge: func(string, string, string) error { return nil },
			whoami: func(string, string) (*beeperapi.RespWhoami, error) {
				close(whoamiStarted)
				<-releaseWhoami
				return absentWhoami("", "")
			},
			wait: noWait,
		})
	}()
	<-whoamiStarted
	cancel()

	var err error
	select {
	case err = <-deleteDone:
	case <-time.After(time.Second):
		t.Fatal("blocked whoami prevented prompt cancellation")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected verification cancellation, got %v", err)
	}
}

func TestIsHTTPNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "typed 404", err: fakeHTTPStatusError{status: http.StatusNotFound}, want: true},
		{name: "typed non-404", err: fakeHTTPStatusError{status: http.StatusForbidden}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isHTTPNotFound(test.err); got != test.want {
				t.Fatalf("isHTTPNotFound(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestIsBeeperNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "typed 404", err: fakeHTTPStatusError{status: http.StatusNotFound}, want: true},
		{name: "error body", err: errors.New("server returned error (HTTP 404): missing"), want: true},
		{name: "bare status", err: errors.New("unexpected status code 404"), want: true},
		{name: "different status", err: errors.New("unexpected status code 4040"), want: false},
		{name: "unrelated prefix", err: errors.New("wrapped: server returned error (HTTP 404): missing"), want: false},
		{name: "404 in unrelated text", err: errors.New("request 404 timed out"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isBeeperNotFoundError(test.err); got != test.want {
				t.Fatalf("isBeeperNotFoundError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestIsBridgeAbsentRejectsMissingBridgesMap(t *testing.T) {
	absent, err := isBridgeAbsent(context.Background(), "sh-imessage", "token", func(string, string) (*beeperapi.RespWhoami, error) {
		return &beeperapi.RespWhoami{}, nil
	})
	if err == nil || absent {
		t.Fatalf("missing bridges map returned absent=%t, error=%v", absent, err)
	}
}

func TestIsBridgeAbsentRejectsNilWhoamiResponse(t *testing.T) {
	absent, err := isBridgeAbsent(context.Background(), "sh-imessage", "token", func(string, string) (*beeperapi.RespWhoami, error) {
		return nil, nil
	})
	if err == nil || absent {
		t.Fatalf("nil whoami returned absent=%t, error=%v", absent, err)
	}
}
