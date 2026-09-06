package connector

import "testing"

func TestAnyNameWordFuzzyMatches(t *testing.T) {
	for _, tc := range []struct {
		query, name string
		want        bool
	}{
		{"jo", "johnson", true},
		{"john", "johnson", true},
		{"jon", "john", true},
		{"john", "jon", true},
		{"johnsen", "johnson", true},
		{"johnson", "jonson", true},
		{"johnson", "jonso", true},
		{"johnson", "jono", false},
		{"jon", "anderson", false},
		{"johnson", "jo", false},
		{"jo", "bo", false},
		{"josé", "jose", false}, // Matching retains the existing byte-based distance.
	} {
		t.Run(tc.query+"/"+tc.name, func(t *testing.T) {
			if got := anyNameWordFuzzyMatches(tc.query, []string{tc.name}); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if !anyNameWordFuzzyMatches("jon", []string{"christopher", "john"}) {
		t.Fatal("an impossible earlier name word must not hide a later match")
	}
}
