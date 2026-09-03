package connector

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
)

func TestRustLogSinkForwardsLevelTargetAndMessage(t *testing.T) {
	for _, tc := range []struct{ rust, want string }{
		{"ERROR", "error"}, {"WARN", "warn"}, {"INFO", "info"},
		{"DEBUG", "debug"}, {"TRACE", "trace"}, {"unknown", "info"},
	} {
		t.Run(tc.rust, func(t *testing.T) {
			var output bytes.Buffer
			sink := rustLogSink{log: zerolog.New(&output).Level(zerolog.TraceLevel)}
			sink.Log(tc.rust, "rustpush::synthetic", "synthetic diagnostic")
			var record map[string]string
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != tc.want || record["target"] != "rustpush::synthetic" || record["message"] != "synthetic diagnostic" {
				t.Fatalf("unexpected forwarded record: %v", record)
			}
		})
	}
}
