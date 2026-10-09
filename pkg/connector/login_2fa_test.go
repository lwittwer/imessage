package connector

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"

	"github.com/lrhodin/corten-matrix/pkg/rustpushgo"
)

// A mistyped 2FA code used to end the login: SubmitUserInput returned an
// error, and every front end (terminal login, provisioning API, Matrix
// commands) drops the login on an error. These pin that a rejected code offers
// the 2FA step again instead, and that the retries are capped so a login can't
// keep sending codes to Apple. None of it talks to Apple.

// fakeVerify answers Submit2fa from a script and records the codes it saw.
type fakeVerify struct {
	answers []func() (bool, error)
	codes   []string
}

func (f *fakeVerify) verify(code string) (bool, error) {
	f.codes = append(f.codes, code)
	answer := f.answers[len(f.codes)-1]
	return answer()
}

func accepted() (bool, error) { return true, nil }

// rejected is how a wrong code reaches Go: verify_2fa fails in check_error and
// verify_sms_2fa returns Bad2faCode.
func rejected() (bool, error) {
	return false, rustpushgo.NewWrappedErrorGenericError("2FA verification failed: Bad2faCode")
}

func failedWith(msg string) func() (bool, error) {
	return func() (bool, error) { return false, errors.New(msg) }
}

var finishedStep = &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeComplete, StepID: LoginStepComplete}

func requireTwoFactorStep(t *testing.T, step *bridgev2.LoginStep, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("login ended with an error, want the 2FA step again: %v", err)
	}
	if step == nil || step.StepID != LoginStepTwoFactor || step.Type != bridgev2.LoginStepTypeUserInput {
		t.Fatalf("got step %+v, want the 2FA user-input step", step)
	}
	if step.UserInputParams == nil || len(step.UserInputParams.Fields) != 1 || step.UserInputParams.Fields[0].ID != "code" {
		t.Fatalf("2FA step must ask for exactly the code field, got %+v", step.UserInputParams)
	}
}

func TestTwoFactorRejectedCodeIsOfferedAgain(t *testing.T) {
	p := twoFactorPrompt{instructions: "Enter your Apple ID verification code."}
	f := &fakeVerify{answers: []func() (bool, error){rejected, accepted}}
	finished := 0
	finish := func() (*bridgev2.LoginStep, error) { finished++; return finishedStep, nil }

	step, err := p.submit(zerolog.Nop(), "111111", f.verify, finish)
	requireTwoFactorStep(t, step, err)
	if finished != 0 {
		t.Fatal("finish ran for a rejected code")
	}
	if !strings.Contains(step.Instructions, "didn't work") || !strings.Contains(step.Instructions, "attempt 2 of 3") {
		t.Errorf("retry instructions don't say what happened: %q", step.Instructions)
	}
	if !strings.HasSuffix(step.Instructions, p.instructions) {
		t.Errorf("retry dropped the original instructions: %q", step.Instructions)
	}

	step, err = p.submit(zerolog.Nop(), "222222", f.verify, finish)
	if err != nil || step != finishedStep || finished != 1 {
		t.Fatalf("a correct second code must continue the login: step=%+v err=%v finished=%d", step, err, finished)
	}
	if strings.Join(f.codes, ",") != "111111,222222" {
		t.Errorf("Apple saw codes %v", f.codes)
	}
}

func TestTwoFactorRetryHidesApplesErrorText(t *testing.T) {
	p := twoFactorPrompt{instructions: "x"}
	f := &fakeVerify{answers: []func() (bool, error){
		failedWith("2FA verification failed: AuthSrpWithMessage(-21669, \"Incorrect verification code.\")"),
	}}
	step, err := p.submit(zerolog.Nop(), "111111", f.verify, nil)
	requireTwoFactorStep(t, step, err)
	if strings.Contains(step.Instructions, "21669") || strings.Contains(step.Instructions, "AuthSrp") {
		t.Errorf("Apple's error text leaked into the prompt: %q", step.Instructions)
	}
	if !strings.HasPrefix(step.Instructions, "That code didn't work.") {
		t.Errorf("instructions %q", step.Instructions)
	}
	if p.attempts != 1 {
		t.Errorf("known incorrect-code response used %d attempts, want 1", p.attempts)
	}
}

func TestTwoFactorRecognizesUpstreamBadCodeDisplay(t *testing.T) {
	for _, reason := range []string{
		"2FA verification failed: Bad 2fa code.",
		"2FA verification failed: Incorrect verification code. (-21669)",
	} {
		t.Run(reason, func(t *testing.T) {
			p := twoFactorPrompt{instructions: "x"}
			f := &fakeVerify{answers: []func() (bool, error){failedWith(reason)}}
			step, err := p.submit(zerolog.Nop(), "111111", f.verify, nil)
			requireTwoFactorStep(t, step, err)
			if !strings.HasPrefix(step.Instructions, "That code didn't work.") || p.attempts != 1 {
				t.Fatalf("upstream bad-code response should consume one incorrect-code try: instructions=%q attempts=%d", step.Instructions, p.attempts)
			}
		})
	}
}

func TestTwoFactorTransientVerificationErrorsDoNotConsumeAttempts(t *testing.T) {
	p := twoFactorPrompt{instructions: "Enter your Apple ID verification code."}
	f := &fakeVerify{answers: []func() (bool, error){
		failedWith("request failed: connection reset"),
		failedWith("Submit2fa panicked: invalid header"),
		rejected,
		accepted,
	}}
	finish := func() (*bridgev2.LoginStep, error) { return finishedStep, nil }

	for range 2 {
		step, err := p.submit(zerolog.Nop(), "111111", f.verify, finish)
		requireTwoFactorStep(t, step, err)
		if !strings.Contains(step.Instructions, "couldn't verify the code just now") ||
			!strings.Contains(step.Instructions, "wasn't counted as an incorrect code") {
			t.Errorf("transient failure should explain that the code was not counted: %q", step.Instructions)
		}
		if strings.Contains(step.Instructions, "connection reset") || strings.Contains(step.Instructions, "invalid header") {
			t.Errorf("verification error details leaked into the prompt: %q", step.Instructions)
		}
		if p.attempts != 0 {
			t.Fatalf("transient verification failure used %d incorrect-code attempts, want 0", p.attempts)
		}
	}

	step, err := p.submit(zerolog.Nop(), "111111", f.verify, finish)
	requireTwoFactorStep(t, step, err)
	if !strings.HasPrefix(step.Instructions, "That code didn't work.") || p.attempts != 1 {
		t.Fatalf("wrong code should consume one attempt after transient failures: instructions=%q attempts=%d", step.Instructions, p.attempts)
	}

	step, err = p.submit(zerolog.Nop(), "222222", f.verify, finish)
	if err != nil || step != finishedStep {
		t.Fatalf("valid code after a transient failure must continue the same login: step=%+v err=%v", step, err)
	}
	if len(f.codes) != 4 {
		t.Errorf("verify ran %d times, want 4 submissions on the same prompt", len(f.codes))
	}
}

// Once Apple has accepted the code, asking for another one is wrong: the
// retry would run against a session that is already verified.
func TestTwoFactorAcceptedCodeFailuresAreNotRetried(t *testing.T) {
	cases := map[string]func() (bool, error){
		"no PET after an accepted code": func() (bool, error) { return false, nil },
		"re-login after the code failed": func() (bool, error) {
			return false, rustpushgo.NewWrappedErrorGenericError("Post-2FA re-login failed: AuthSrp")
		},
	}
	for name, answer := range cases {
		p := twoFactorPrompt{instructions: "x"}
		f := &fakeVerify{answers: []func() (bool, error){answer}}
		step, err := p.submit(zerolog.Nop(), "123456", f.verify, nil)
		if err == nil || step != nil {
			t.Errorf("%s: got step=%+v err=%v, want the login to end", name, step, err)
		}
	}
}

func TestTwoFactorAttemptsAreCapped(t *testing.T) {
	p := twoFactorPrompt{instructions: "x"}
	f := &fakeVerify{answers: []func() (bool, error){rejected, rejected, rejected, accepted}}
	finish := func() (*bridgev2.LoginStep, error) { t.Fatal("finish ran without an accepted code"); return nil, nil }

	for i := 1; i < maxTwoFactorAttempts; i++ {
		step, err := p.submit(zerolog.Nop(), "123456", f.verify, finish)
		requireTwoFactorStep(t, step, err)
	}
	step, err := p.submit(zerolog.Nop(), "123456", f.verify, finish)
	if err == nil || step != nil {
		t.Fatalf("attempt %d must end the login, got step=%+v err=%v", maxTwoFactorAttempts, step, err)
	}
	if !strings.Contains(err.Error(), "3 times") || !strings.Contains(err.Error(), "start the login again") {
		t.Errorf("error should say the attempts ran out: %v", err)
	}
	if len(f.codes) != maxTwoFactorAttempts {
		t.Errorf("Apple was sent %d codes, want %d", len(f.codes), maxTwoFactorAttempts)
	}
	step, err = p.submit(zerolog.Nop(), "654321", f.verify, finish)
	if err == nil || step != nil || len(f.codes) != maxTwoFactorAttempts {
		t.Errorf("submission after the attempt cap reached Apple or returned a step: step=%+v err=%v codes=%v", step, err, f.codes)
	}
}

func TestTwoFactorEmptyCodeIsAskedAgainWithoutSendingIt(t *testing.T) {
	p := twoFactorPrompt{instructions: "x"}
	f := &fakeVerify{answers: []func() (bool, error){accepted}}
	for _, code := range []string{"", "   ", "\t\n"} {
		step, err := p.submit(zerolog.Nop(), code, f.verify, nil)
		requireTwoFactorStep(t, step, err)
		if !strings.HasPrefix(step.Instructions, "No code was entered.") {
			t.Errorf("%q: instructions %q", code, step.Instructions)
		}
	}
	if len(f.codes) != 0 || p.attempts != 0 {
		t.Fatalf("an empty code must not reach Apple or use an attempt: codes=%v attempts=%d", f.codes, p.attempts)
	}
	step, err := p.submit(zerolog.Nop(), " 123456 \n", f.verify, func() (*bridgev2.LoginStep, error) { return finishedStep, nil })
	if err != nil || step != finishedStep {
		t.Fatalf("got step=%+v err=%v", step, err)
	}
	if f.codes[0] != "123456" {
		t.Errorf("code sent to Apple was %q, want it trimmed", f.codes[0])
	}
}

func TestTwoFactorFinishErrorIsNotRetried(t *testing.T) {
	// Once Apple accepts the code, a failure in IDS registration is not a
	// 2FA problem; asking for another code would be wrong.
	p := twoFactorPrompt{instructions: "x"}
	f := &fakeVerify{answers: []func() (bool, error){accepted}}
	want := errors.New("login completion failed")
	step, err := p.submit(zerolog.Nop(), "123456", f.verify, func() (*bridgev2.LoginStep, error) { return nil, want })
	if !errors.Is(err, want) || step != nil {
		t.Fatalf("got step=%+v err=%v, want finish's error passed through", step, err)
	}
}

// The two login flows route a code submission into the prompt, against the
// session from the password step, and keep routing there on a retry.
func TestLoginFlowsOfferTwoFactorAgainOnVerificationFailure(t *testing.T) {
	main := &IMConnector{Bridge: &bridgev2.Bridge{Log: zerolog.Nop()}}
	for _, name := range []string{"apple-id", "external-key"} {
		t.Run(name, func(t *testing.T) {
			session := &rustpushgo.LoginSession{}
			var sent []string
			orig := submit2fa
			t.Cleanup(func() { submit2fa = orig })
			submit2fa = func(s *rustpushgo.LoginSession, code string) (bool, error) {
				if s != session {
					t.Fatal("2FA code was sent against a different session")
				}
				sent = append(sent, code)
				if len(sent) == 1 {
					return false, errors.New("connection reset")
				}
				return false, rustpushgo.NewWrappedErrorGenericError("2FA verification failed: Bad2faCode")
			}
			var flow bridgev2.LoginProcessUserInput
			if name == "apple-id" {
				flow = &AppleIDLogin{Main: main, session: session, twoFactor: twoFactorPrompt{instructions: "x"}}
			} else {
				flow = &ExternalKeyLogin{Main: main, cfg: &rustpushgo.WrappedOsConfig{}, session: session,
					twoFactor: twoFactorPrompt{instructions: "x"}}
			}
			step, err := flow.SubmitUserInput(t.Context(), map[string]string{"code": "123456"})
			requireTwoFactorStep(t, step, err)
			if !strings.Contains(step.Instructions, "wasn't counted as an incorrect code") {
				t.Fatalf("transient error did not preserve the 2FA step: %q", step.Instructions)
			}

			for i := 1; i < maxTwoFactorAttempts; i++ {
				step, err = flow.SubmitUserInput(t.Context(), map[string]string{"code": "123456"})
				requireTwoFactorStep(t, step, err)
			}
			step, err = flow.SubmitUserInput(t.Context(), map[string]string{"code": "123456"})
			if err == nil || step != nil {
				t.Fatalf("attempt %d must end the login, got step=%+v err=%v", maxTwoFactorAttempts, step, err)
			}
			if len(sent) != maxTwoFactorAttempts+1 {
				t.Errorf("sent %d codes, want one transient retry plus %d incorrect-code attempts", len(sent), maxTwoFactorAttempts)
			}
		})
	}
}

// The provisioning API (the Beeper app's login) only shows an error's text when
// it can write itself as an HTTP error; anything else becomes "Internal error
// submitting input". So every error that ends the login at the 2FA step must
// be one.
func TestTwoFactorFailuresReachTheBeeperApp(t *testing.T) {
	cases := map[string][]func() (bool, error){
		"out of attempts": {rejected, rejected, rejected},
		"no PET":          {func() (bool, error) { return false, nil }},
		"re-login failed": {func() (bool, error) {
			return false, rustpushgo.NewWrappedErrorGenericError("Post-2FA re-login failed: AuthSrp")
		}},
	}
	for name, answers := range cases {
		p := twoFactorPrompt{instructions: "x"}
		f := &fakeVerify{answers: answers}
		var err error
		for range answers {
			_, err = p.submit(zerolog.Nop(), "123456", f.verify, nil)
		}
		var we interface{ Write(http.ResponseWriter) }
		if !errors.As(err, &we) {
			t.Errorf("%s: %T would show as a generic error in the Beeper app", name, err)
			continue
		}
		rec := httptest.NewRecorder()
		we.Write(rec)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "start the login again") {
			t.Errorf("%s: provisioning would answer %d %s", name, rec.Code, rec.Body.String())
		}
	}
}
