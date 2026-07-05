package connector

import "testing"

func TestQueuedPortalWatermarkKeepsMessageAndMetadataSeparate(t *testing.T) {
	watermark := queuedPortalWatermark{}.withPortal(portalWithNewestMessage{
		ActivityTS: 10000,
	})
	messageCandidate := portalWithNewestMessage{
		NewestTS:   5000,
		ActivityTS: 10000,
	}
	if watermark.covers(messageCandidate) {
		t.Fatalf("metadata-only watermark %#v covered later message candidate %#v", watermark, messageCandidate)
	}

	watermark = watermark.withPortal(messageCandidate)
	if !watermark.covers(messageCandidate) {
		t.Fatalf("updated watermark %#v did not cover queued message candidate %#v", watermark, messageCandidate)
	}

	metadataCandidate := portalWithNewestMessage{
		NewestTS:   5000,
		ActivityTS: 11000,
	}
	if watermark.covers(metadataCandidate) {
		t.Fatalf("message watermark %#v covered later metadata candidate %#v", watermark, metadataCandidate)
	}
}

func TestBackfillTriggerTimestampIncludesReactionOnlyActivity(t *testing.T) {
	tests := []struct {
		name string
		info portalWithNewestMessage
		want int64
	}{
		{
			name: "contentful message",
			info: portalWithNewestMessage{
				NewestTS:        5000,
				ActivityTS:      7000,
				MessageCount:    2,
				ContentfulCount: 1,
			},
			want: 5000,
		},
		{
			name: "reaction only message rows",
			info: portalWithNewestMessage{
				ActivityTS:      7000,
				MessageCount:    1,
				ContentfulCount: 0,
			},
			want: 7000,
		},
		{
			name: "metadata only chat row",
			info: portalWithNewestMessage{
				ActivityTS: 7000,
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backfillTriggerTimestamp(tt.info); got != tt.want {
				t.Fatalf("backfillTriggerTimestamp(%#v) = %d, want %d", tt.info, got, tt.want)
			}
		})
	}
}
