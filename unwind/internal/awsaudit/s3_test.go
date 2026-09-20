package awsaudit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubS3 stands in for S3: it captures the PutObject the SDK actually sends,
// so these tests exercise real credential resolution, SigV4 signing and
// path-style addressing without an AWS account or a Docker daemon.
type stubS3 struct {
	server  *httptest.Server
	path    string
	body    []byte
	headers http.Header
}

func newStubS3(t *testing.T) *stubS3 {
	t.Helper()
	s := &stubS3{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.path, s.body, s.headers = r.URL.Path, body, r.Header.Clone()
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("x-amz-version-id", "v-0001")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func newTestExporter(t *testing.T, endpoint string) *Exporter {
	t.Helper()
	t.Setenv("AWS_S3_BUCKET", "unwind-audit-test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	exp, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !exp.Enabled() {
		t.Fatal("exporter should be enabled when AWS_S3_BUCKET is set")
	}
	return exp
}

// Without a bucket configured, archiving is absent rather than broken -- the
// local demo must never depend on AWS being reachable.
func TestNew_NoBucketMeansDisabled(t *testing.T) {
	t.Setenv("AWS_S3_BUCKET", "")
	exp, err := New(context.Background())
	if err != nil {
		t.Fatalf("New should not error when unconfigured: %v", err)
	}
	if exp.Enabled() {
		t.Fatal("exporter should be disabled with no bucket")
	}
	if exp.Bucket() != "" {
		t.Fatal("disabled exporter should report no bucket")
	}
	if _, err := exp.Export(context.Background(), "s1", nil, nil); err == nil {
		t.Fatal("Export on a disabled exporter should return an error, not panic")
	}
}

func TestExport_WritesAuditRecord(t *testing.T) {
	stub := newStubS3(t)
	exp := newTestExporter(t, stub.server.URL)

	session := map[string]any{"id": "rogue-abc", "status": "rolled_back"}
	intents := []map[string]any{
		{"seq": 1, "tool": "cancel_subscription", "status": "compensated"},
		{"seq": 2, "tool": "transfer_funds", "status": "uncompensable"},
	}

	res, err := exp.Export(context.Background(), "rogue-abc", session, intents)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Path-style addressing puts the bucket in the path, which is what makes
	// LocalStack and other S3-compatible endpoints work.
	if want := "/unwind-audit-test/sessions/rogue-abc.json"; stub.path != want {
		t.Errorf("request path = %q, want %q", stub.path, want)
	}
	if res.Key != "sessions/rogue-abc.json" {
		t.Errorf("key = %q", res.Key)
	}
	if res.URI != "s3://unwind-audit-test/sessions/rogue-abc.json" {
		t.Errorf("uri = %q", res.URI)
	}
	if res.VersionID != "v-0001" {
		t.Errorf("version id = %q, want v-0001 (versioning is what makes this an audit trail)", res.VersionID)
	}
	if res.Bytes != len(stub.body) {
		t.Errorf("reported %d bytes, sent %d", res.Bytes, len(stub.body))
	}

	// The SDK must have actually signed the request.
	if auth := stub.headers.Get("Authorization"); !strings.Contains(auth, "AWS4-HMAC-SHA256") {
		t.Errorf("request was not SigV4-signed: %q", auth)
	}

	// The record has to be self-contained: an auditor reading this object
	// alone should be able to reconstruct the session.
	var rec Record
	if err := json.Unmarshal(stub.body, &rec); err != nil {
		t.Fatalf("uploaded body is not valid JSON: %v", err)
	}
	if rec.SchemaVersion != "unwind.audit.v1" {
		t.Errorf("schema version = %q", rec.SchemaVersion)
	}
	if rec.ArchivedAt == "" {
		t.Error("archived_at must be set")
	}
	if len(rec.Intents) != 2 {
		t.Fatalf("archived %d intents, want 2", len(rec.Intents))
	}
	if rec.Intents[1]["status"] != "uncompensable" {
		t.Errorf("intent detail lost in archive: %+v", rec.Intents[1])
	}
}

// Re-archiving the same session must land on the same key: with bucket
// versioning on, that preserves every prior version rather than scattering
// near-duplicate objects.
func TestExport_KeyIsStablePerSession(t *testing.T) {
	stub := newStubS3(t)
	exp := newTestExporter(t, stub.server.URL)

	first, err := exp.Export(context.Background(), "s-42", map[string]any{"id": "s-42"}, nil)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	second, err := exp.Export(context.Background(), "s-42", map[string]any{"id": "s-42"}, nil)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if first.Key != second.Key {
		t.Errorf("key changed between exports: %q then %q", first.Key, second.Key)
	}
}
