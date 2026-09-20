// Package awsaudit writes each session's ledger to Amazon S3 as a
// tamper-evident audit record.
//
// Why this exists: the SQLite ledger proves what an agent did, but it sits on
// the same machine as the agent's blast radius -- anything that can reach the
// process can reach the file. Exporting each settled session to versioned S3
// (ideally with Object Lock in COMPLIANCE mode) puts the record somewhere the
// agent, the operator, and this process cannot rewrite or delete. That is the
// difference between a log and an audit trail.
//
// Everything here degrades to absent: with no bucket configured, Exporter is
// nil and every caller no-ops. The local demo never depends on AWS being
// reachable.
package awsaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Exporter writes audit records to one S3 bucket.
type Exporter struct {
	client *s3.Client
	bucket string
	prefix string
}

// Result describes where a record landed, so the UI and API can show that the
// export is real rather than claimed.
type Result struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"version_id,omitempty"`
	ETag      string `json:"etag,omitempty"`
	Bytes     int    `json:"bytes"`
	URI       string `json:"uri"`
}

// New builds an Exporter from the environment, or returns nil (no error) when
// S3 archiving simply isn't configured -- the normal case for a local demo.
//
//	AWS_S3_BUCKET       required; absent means "feature off"
//	AWS_REGION          defaults to us-east-1
//	AWS_S3_PREFIX       defaults to "sessions"
//	AWS_ENDPOINT_URL    optional; point at LocalStack to test with no account
//
// Credentials are resolved by the SDK's normal chain (env vars, shared config
// file, instance role), so this works unchanged in a container on ECS.
func New(ctx context.Context) (*Exporter, error) {
	bucket := os.Getenv("AWS_S3_BUCKET")
	if bucket == "" {
		return nil, nil
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		// LocalStack (and any S3-compatible endpoint) needs an override plus
		// path-style addressing, since bucket-as-subdomain doesn't resolve
		// against localhost.
		if endpoint := os.Getenv("AWS_ENDPOINT_URL"); endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})

	prefix := os.Getenv("AWS_S3_PREFIX")
	if prefix == "" {
		prefix = "sessions"
	}
	return &Exporter{client: client, bucket: bucket, prefix: prefix}, nil
}

// Enabled reports whether archiving is configured. A nil Exporter is the
// "not configured" state, so this is safe on a nil receiver.
func (e *Exporter) Enabled() bool { return e != nil }

// Bucket is the configured bucket name, for display.
func (e *Exporter) Bucket() string {
	if e == nil {
		return ""
	}
	return e.bucket
}

// Record is the audit document written to S3: the session, every intent in
// seq order (args, result, and the captured compensation record), and when it
// was archived. It is deliberately self-contained -- an auditor reading this
// object needs nothing else to reconstruct what the agent did and what was
// reversed.
type Record struct {
	SchemaVersion string           `json:"schema_version"`
	ArchivedAt    string           `json:"archived_at"`
	Session       any              `json:"session"`
	Intents       []map[string]any `json:"intents"`
}

// Export writes one session's audit record and returns where it landed.
// The key is deterministic per session, so re-archiving the same session
// overwrites in place -- with bucket versioning on, "overwrite" still
// preserves every prior version, which is the point.
func (e *Exporter) Export(ctx context.Context, sessionID string, session any, intents []map[string]any) (*Result, error) {
	if e == nil {
		return nil, fmt.Errorf("S3 archiving is not configured")
	}

	body, err := json.MarshalIndent(Record{
		SchemaVersion: "unwind.audit.v1",
		ArchivedAt:    time.Now().UTC().Format(time.RFC3339),
		Session:       session,
		Intents:       intents,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal audit record: %w", err)
	}

	key := fmt.Sprintf("%s/%s.json", e.prefix, sessionID)
	out, err := e.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(e.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		Metadata: map[string]string{
			"unwind-session": sessionID,
			"unwind-intents": fmt.Sprintf("%d", len(intents)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("put audit record: %w", err)
	}

	res := &Result{
		Bucket: e.bucket,
		Key:    key,
		Bytes:  len(body),
		URI:    fmt.Sprintf("s3://%s/%s", e.bucket, key),
	}
	if out.VersionId != nil {
		res.VersionID = *out.VersionId
	}
	if out.ETag != nil {
		res.ETag = *out.ETag
	}
	return res, nil
}
