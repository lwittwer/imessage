//go:build darwin

package mac

import "testing"

func TestIsEmailIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "mailto:user@example.com", want: true},
		{input: "user@example.com", want: true},
		{input: "tel:+15551234567", want: false},
		{input: "+15551234567", want: false},
		{input: "15551234567", want: false},
	}

	for _, tt := range tests {
		if got := isEmailIdentifier(tt.input); got != tt.want {
			t.Fatalf("isEmailIdentifier(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
