package connector

import "testing"

func TestCloudSyncCountersHasChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts cloudSyncCounters
		want   bool
	}{
		{"empty", cloudSyncCounters{}, false},
		{"skipped only", cloudSyncCounters{Skipped: 1}, false},
		// A filtered chat is still a written row, and a portal candidate when
		// bridge_filtered_chats is on.
		{"filtered only", cloudSyncCounters{Filtered: 1}, true},
		{"imported", cloudSyncCounters{Imported: 1}, true},
		{"updated", cloudSyncCounters{Updated: 1}, true},
		{"deleted", cloudSyncCounters{Deleted: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.counts.hasChanges(); got != tc.want {
				t.Errorf("hasChanges() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRunDelayedCloudReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		counts                      cloudSyncCounters
		currentPassFailed           bool
		previousPassFailed          bool
		portalReconciliationPending bool
		want                        bool
	}{
		{"successful empty pass", cloudSyncCounters{}, false, false, false, false},
		{"current pass failed after partial writes", cloudSyncCounters{}, true, false, false, true},
		{"current failed pass reported writes", cloudSyncCounters{Imported: 1}, true, false, false, true},
		{"previous pass failed", cloudSyncCounters{}, false, true, false, true},
		{"portal scan pending", cloudSyncCounters{}, false, false, true, true},
		{"both failures pending", cloudSyncCounters{}, false, true, true, true},
		{"successful pass wrote changes", cloudSyncCounters{Updated: 1}, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRunDelayedCloudReconciliation(tc.counts, tc.currentPassFailed, tc.previousPassFailed, tc.portalReconciliationPending); got != tc.want {
				t.Errorf("shouldRunDelayedCloudReconciliation() = %v, want %v", got, tc.want)
			}
		})
	}
}
