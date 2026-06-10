package main

import "testing"

func TestIMessageBridgeIconUsesMatrixMediaURI(t *testing.T) {
	if iMessageBridgeIconMXC != "mxc://maunium.net/tManJEpANASZvDVzvRvhILdX" {
		t.Fatalf("iMessageBridgeIconMXC = %q, want official mautrix iMessage icon", iMessageBridgeIconMXC)
	}
}
