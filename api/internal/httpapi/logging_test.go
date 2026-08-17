package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/azure/azure-functions-golang-worker/sdk"
)

// captureLogs points the default logger at a buffer for the duration of one test
// and restores it afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buf
}

// findRecord returns the first captured record with the given message.
func findRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()

	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if record["msg"] == msg {
			return record
		}
	}

	t.Fatalf("no log record with msg %q in:\n%s", msg, buf.String())
	return nil
}

func TestLogQueryIsSafeToEmit(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"ordinary query passes through", "scaling single-threaded servers", "scaling single-threaded servers"},
		{"newlines cannot forge a record", "real\n{\"msg\":\"fake\"}", "real {\"msg\":\"fake\"}"},
		{"carriage return folded", "a\rb", "a b"},
		{"tab folded", "a\tb", "a b"},
		{"null byte folded", "a\x00b", "a b"},
		{"empty stays empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logQuery(tc.in); got != tc.want {
				t.Errorf("logQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogQueryTruncatesOnARuneBoundary(t *testing.T) {
	// Multi-byte throughout: a byte-wise cut would land mid-character and emit
	// mojibake into the logs.
	long := strings.Repeat("é", maxLoggedQuery+50)

	got := logQuery(long)
	trimmed := strings.TrimSuffix(got, "…")
	if trimmed == got {
		t.Fatal("an over-long query should be marked as truncated")
	}
	if n := len([]rune(trimmed)); n != maxLoggedQuery {
		t.Errorf("kept %d runes, want %d", n, maxLoggedQuery)
	}
	if !strings.HasPrefix(long, trimmed) {
		t.Error("truncation split a character rather than cutting between them")
	}
}

func TestSearchLogsOneLineWithTheOutcome(t *testing.T) {
	buf := captureLogs(t)
	h := newTestHandlers(t, `{"@odata.count":7,"value":[{"url":"https://example.com/a","title":"A"}]}`)

	// The query arrives carrying a newline, which is the shape a log-injection
	// attempt takes: it must reach the record as one field, not two lines.
	if code := get(t, h, "/api/search?q=go%0Aroutines").Code; code != http.StatusOK {
		t.Fatalf("got status %d, want 200", code)
	}

	record := findRecord(t, buf, "search")
	for field, want := range map[string]any{
		"query": "go routines",
		"count": float64(1),
		"total": float64(7),
		"rank":  "keyword",
	} {
		if record[field] != want {
			t.Errorf("%s = %v, want %v", field, record[field], want)
		}
	}
	if _, ok := record["duration_ms"]; !ok {
		t.Error("the outcome line should carry duration_ms")
	}
}

func TestThrottledSearchIsLogged(t *testing.T) {
	buf := captureLogs(t)
	h := newTestHandlers(t, `{"@odata.count":0,"value":[]}`)
	h.perClient = newLimiter(60, 1)

	get(t, h, "/api/search?q=go")
	if code := get(t, h, "/api/search?q=go").Code; code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", code)
	}

	record := findRecord(t, buf, "search throttled")
	if _, ok := record["caller"]; !ok {
		t.Error("a throttle record should name the caller, or it cannot be acted on")
	}
}

// The Functions SDK installs its own slog handler at import time, and that handler
// is what attaches invocation_id and routes records to the host over gRPC. Calling
// slog.SetDefault with a handler of our own would silently replace it: logs would
// fall back to stderr and lose the correlation that makes them searchable in
// Application Insights.
//
// This test pins the behaviour we depend on rather than a handler we wrote.
func TestInvocationContextReachesLogRecords(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(sdk.NewLogHandler(slog.NewJSONHandler(&buf, nil)))

	ctx := sdk.NewContext(context.Background(), &sdk.InvocationContext{
		InvocationID: "inv-123",
		FunctionName: "search",
	})
	logger.InfoContext(ctx, "search", "count", 1)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	if record["invocation_id"] != "inv-123" {
		t.Errorf("invocation_id = %v, want inv-123", record["invocation_id"])
	}
	if record["function_name"] != "search" {
		t.Errorf("function_name = %v, want search", record["function_name"])
	}
}
