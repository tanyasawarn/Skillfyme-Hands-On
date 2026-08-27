package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3Client is a minimal, in-memory S3Client for tests -- captures
// every PutObject call's bucket/key/body without a real network call.
type fakeS3Client struct {
	mu    sync.Mutex
	calls []fakePutObjectCall
	err   error // if set, every PutObject call fails with this error
}

type fakePutObjectCall struct {
	Bucket string
	Key    string
	Body   []byte
}

func (f *fakeS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	body := make([]byte, 0)
	if params.Body != nil {
		buf := make([]byte, 65536)
		n, _ := params.Body.Read(buf)
		body = buf[:n]
	}
	f.calls = append(f.calls, fakePutObjectCall{
		Bucket: *params.Bucket,
		Key:    *params.Key,
		Body:   body,
	})
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) lastCall() (fakePutObjectCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakePutObjectCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

func (f *fakeS3Client) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestS3RecordingSink_WriteThenFlushUploadsToCorrectKey(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour) // long interval, flush manually via flushOne

	sink.Write(context.Background(), "attempt-123", []byte("hello"))
	if err := sink.flushOne(context.Background(), "attempt-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, ok := client.lastCall()
	if !ok {
		t.Fatal("expected a PutObject call")
	}
	if call.Bucket != "test-bucket" {
		t.Errorf("expected bucket=test-bucket, got %s", call.Bucket)
	}
	if call.Key != "recordings/attempt-123.cast" {
		t.Errorf("expected key=recordings/attempt-123.cast, got %s", call.Key)
	}
}

// TestS3RecordingSink_ProducesValidAsciicastHeader confirms the first
// line written is a parseable asciicast v2 header (version 2, per the
// real format spec) -- a malformed header would make every downstream
// asciicast player/tool reject the whole recording.
func TestS3RecordingSink_ProducesValidAsciicastHeader(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte("output"))
	if err := sink.flushOne(context.Background(), "attempt-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, _ := client.lastCall()
	lines := strings.Split(strings.TrimSpace(string(call.Body)), "\n")
	if len(lines) < 1 {
		t.Fatal("expected at least a header line")
	}
	var header asciicastHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header line is not valid JSON: %v", err)
	}
	if header.Version != 2 {
		t.Errorf("expected asciicast version 2, got %d", header.Version)
	}
}

// TestS3RecordingSink_HeaderWrittenOnlyOnce confirms multiple Write
// calls for the same attempt don't each re-emit a header line -- a real
// asciicast file has exactly one header followed by N event lines.
func TestS3RecordingSink_HeaderWrittenOnlyOnce(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte("first"))
	sink.Write(context.Background(), "attempt-123", []byte("second"))
	sink.Write(context.Background(), "attempt-123", []byte("third"))
	if err := sink.flushOne(context.Background(), "attempt-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, _ := client.lastCall()
	lines := strings.Split(strings.TrimSpace(string(call.Body)), "\n")
	// 1 header + 3 events = 4 lines.
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (1 header + 3 events), got %d: %q", len(lines), lines)
	}
	headerCount := 0
	for _, line := range lines {
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err == nil {
			if _, isHeader := probe["version"]; isHeader {
				headerCount++
			}
		}
	}
	if headerCount != 1 {
		t.Errorf("expected exactly 1 header line among %d lines, found %d", len(lines), headerCount)
	}
}

// TestS3RecordingSink_EventLineFormat confirms each event line matches
// asciicast v2's [time, "o", data] array shape -- a different shape
// would silently break every asciicast player.
func TestS3RecordingSink_EventLineFormat(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte("hello world"))
	if err := sink.flushOne(context.Background(), "attempt-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, _ := client.lastCall()
	lines := strings.Split(strings.TrimSpace(string(call.Body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (header + 1 event), got %d", len(lines))
	}
	var event []json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatalf("event line is not a valid JSON array: %v", err)
	}
	if len(event) != 3 {
		t.Fatalf("expected a 3-element event array [time, type, data], got %d elements", len(event))
	}
	var eventType string
	if err := json.Unmarshal(event[1], &eventType); err != nil || eventType != "o" {
		t.Errorf("expected event type \"o\" (output), got %v", eventType)
	}
	var data string
	if err := json.Unmarshal(event[2], &data); err != nil || data != "hello world" {
		t.Errorf("expected event data \"hello world\", got %v", data)
	}
}

func TestS3RecordingSink_FlushOneNoOpsWhenNothingBuffered(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	// No Write call at all for this attempt.
	if err := sink.flushOne(context.Background(), "never-written"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.callCount() != 0 {
		t.Errorf("expected no PutObject calls for an attempt with nothing buffered, got %d", client.callCount())
	}
}

func TestS3RecordingSink_WriteWithEmptyDataIsNoOp(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte{})
	if err := sink.flushOne(context.Background(), "attempt-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.callCount() != 0 {
		t.Errorf("expected no PutObject call for an empty-data Write, got %d calls", client.callCount())
	}
}

// TestS3RecordingSink_Forget_FlushesBeforeDropping is a regression test
// for a real bug caught before shipping: an earlier version of Forget
// just deleted the in-memory buffer without flushing it first, silently
// losing up to flushInterval's worth of trailing output -- exactly the
// most recent (often most relevant) part of a recording, lost right when
// an environment is torn down.
func TestS3RecordingSink_Forget_FlushesBeforeDropping(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour) // long interval -- would never auto-flush in this test's timeframe

	sink.Write(context.Background(), "attempt-123", []byte("final output before teardown"))
	sink.Forget("attempt-123")

	if client.callCount() != 1 {
		t.Fatalf("expected Forget to trigger exactly one final flush, got %d PutObject calls", client.callCount())
	}
	call, _ := client.lastCall()
	if !strings.Contains(string(call.Body), "final output before teardown") {
		t.Error("expected the final flush to include the buffered output written just before Forget")
	}
}

func TestS3RecordingSink_Forget_DropsBufferAfterFlush(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte("data"))
	sink.Forget("attempt-123")

	sink.mu.Lock()
	_, stillPresent := sink.sessions["attempt-123"]
	sink.mu.Unlock()
	if stillPresent {
		t.Error("expected the session buffer to be dropped after Forget")
	}
}

func TestS3RecordingSink_MultipleAttemptsAreIndependent(t *testing.T) {
	client := &fakeS3Client{}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-a", []byte("data for a"))
	sink.Write(context.Background(), "attempt-b", []byte("data for b"))

	if err := sink.flushOne(context.Background(), "attempt-a"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := sink.flushOne(context.Background(), "attempt-b"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.callCount() != 2 {
		t.Fatalf("expected 2 separate PutObject calls, got %d", client.callCount())
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.calls[0].Key == client.calls[1].Key {
		t.Error("expected different attempts to flush to different object keys")
	}
}

func TestS3RecordingSink_FlushErrorIsReturned(t *testing.T) {
	client := &fakeS3Client{err: context.DeadlineExceeded}
	sink := NewS3RecordingSink(client, "test-bucket", time.Hour)

	sink.Write(context.Background(), "attempt-123", []byte("data"))
	err := sink.flushOne(context.Background(), "attempt-123")
	if err == nil {
		t.Fatal("expected the S3 client's error to propagate")
	}
}

func TestNewS3RecordingSink_DefaultsFlushIntervalWhenNonPositive(t *testing.T) {
	sink := NewS3RecordingSink(&fakeS3Client{}, "test-bucket", 0)
	if sink.flushInterval != 5*time.Second {
		t.Errorf("expected default flush interval of 5s, got %s", sink.flushInterval)
	}

	sinkNeg := NewS3RecordingSink(&fakeS3Client{}, "test-bucket", -time.Second)
	if sinkNeg.flushInterval != 5*time.Second {
		t.Errorf("expected default flush interval of 5s for a negative input, got %s", sinkNeg.flushInterval)
	}
}
