package connector

import (
	"reflect"
	"testing"
)

func TestPlanCarrierGroupConsolidation(t *testing.T) {
	comma := "tel:+15551111111,tel:+15552222222"
	commaB := "tel:+15553333333,tel:+15554444444"

	tests := []struct {
		name    string
		entries []carrierConsolidationEntry
		want    []carrierConsolidationGroup
	}{
		{
			name: "multiple gid encodings collapse to one canonical group",
			entries: []carrierConsolidationEntry{
				{portalID: "gid:aaaa", canonical: comma},
				{portalID: "gid:bbbb", canonical: comma},
				{portalID: "gid:cccc", canonical: comma},
			},
			want: []carrierConsolidationGroup{
				{canonical: comma, members: []string{"gid:aaaa", "gid:bbbb", "gid:cccc"}},
			},
		},
		{
			name: "single gid still re-keyed to participant form",
			entries: []carrierConsolidationEntry{
				{portalID: "gid:aaaa", canonical: comma},
			},
			want: []carrierConsolidationGroup{
				{canonical: comma, members: []string{"gid:aaaa"}},
			},
		},
		{
			name: "already-canonical single portal is a no-op",
			entries: []carrierConsolidationEntry{
				{portalID: comma, canonical: comma},
			},
			want: nil,
		},
		{
			name: "comma survivor plus gid duplicate still consolidates",
			entries: []carrierConsolidationEntry{
				{portalID: comma, canonical: comma},
				{portalID: "gid:aaaa", canonical: comma},
			},
			want: []carrierConsolidationGroup{
				{canonical: comma, members: []string{"gid:aaaa", comma}},
			},
		},
		{
			name: "distinct participant sets stay separate",
			entries: []carrierConsolidationEntry{
				{portalID: "gid:aaaa", canonical: comma},
				{portalID: "gid:bbbb", canonical: comma},
				{portalID: "gid:cccc", canonical: commaB},
				{portalID: "gid:dddd", canonical: commaB},
			},
			want: []carrierConsolidationGroup{
				{canonical: comma, members: []string{"gid:aaaa", "gid:bbbb"}},
				{canonical: commaB, members: []string{"gid:cccc", "gid:dddd"}},
			},
		},
		{
			name: "duplicate portal IDs are de-duplicated within a group",
			entries: []carrierConsolidationEntry{
				{portalID: "gid:aaaa", canonical: comma},
				{portalID: "gid:aaaa", canonical: comma},
				{portalID: "gid:bbbb", canonical: comma},
			},
			want: []carrierConsolidationGroup{
				{canonical: comma, members: []string{"gid:aaaa", "gid:bbbb"}},
			},
		},
		{
			name: "entries with empty canonical or portal are ignored",
			entries: []carrierConsolidationEntry{
				{portalID: "gid:aaaa", canonical: ""},
				{portalID: "", canonical: comma},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planCarrierGroupConsolidation(tt.entries)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("planCarrierGroupConsolidation() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
