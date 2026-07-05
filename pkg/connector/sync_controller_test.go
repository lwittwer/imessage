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
