package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client is the narrow subset of *s3.Client this package needs --
// letting tests substitute a fake without pulling in a mocked HTTP
// transport for the whole AWS SDK. *s3.Client satisfies this trivially
// (structural typing).
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// asciicastHeader is asciicast v2 format's required first line -- doc
// §5.4: "record asciicast to S3". Real asciicast v2 spec:
// https://docs.asciinema.org/manual/asciicast/v2/ -- a JSON header line
// followed by one JSON-array event line per output chunk,
// [relative_time_seconds, "o", data].
type asciicastHeader struct {
	Version   int            `json:"version"`
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	Timestamp int64          `json:"timestamp"`
	Env       map[string]any `json:"env,omitempty"`
}

// S3RecordingSink implements sessionbroker.RecordingSink by buffering
// each attempt's terminal output in memory as real asciicast v2 lines,
// periodically flushing the accumulated buffer to S3-compatible object
// storage as a single object per attempt (overwritten on each flush --
// PutObject, not multipart append; see this file's package doc for why
// that trade-off was chosen over true incremental multipart upload).
//
// Replaces LogRecordingSink (nats_sink.go), which discarded every byte
// -- "the tap point exists and is wired... actual S3 upload is a thin
// adapter left for deployment-time configuration" was true as written,
// but nothing ever built that adapter. This is that adapter, made real:
// works against any S3-compatible endpoint (real AWS S3, or MinIO for
// local dev/docker-compose -- same S3Client interface, only the
// aws.Config's BaseEndpoint differs).
type S3RecordingSink struct {
	client S3Client
	bucket string

	// flushInterval controls how often each attempt's buffer is flushed
	// to S3 -- doc's own retention framing (§4.3: 30-90 day lifecycle)
	// implies recordings are read well after the fact, not tailed live,
	// so a periodic flush (not per-chunk) is the right trade: fewer S3
	// PUT calls, acceptable data-loss window (at most one flush
	// interval's worth of output lost if the orchestrator crashes
	// between flushes -- the same trade every buffered-log system makes).
	flushInterval time.Duration

	mu       sync.Mutex
	sessions map[string]*recordingSession
}

type recordingSession struct {
	buf        bytes.Buffer
	startedAt  time.Time
	lastEvent  time.Time
	headerSent bool
}

// NewS3RecordingSink builds a sink against the given S3-compatible
// client/bucket. flushInterval <= 0 defaults to 5 seconds.
func NewS3RecordingSink(client S3Client, bucket string, flushInterval time.Duration) *S3RecordingSink {
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}
	sink := &S3RecordingSink{
		client:        client,
		bucket:        bucket,
		flushInterval: flushInterval,
		sessions:      make(map[string]*recordingSession),
	}
	return sink
}

// Write implements sessionbroker.RecordingSink. Appends data as one
// asciicast v2 event line to attemptID's in-memory buffer -- actual S3
// upload happens on the next periodic flush (see Run), not synchronously
// on every Write call, so this stays cheap enough to call from the same
// hot path telemetry_tap.go already runs on for every chunk of terminal
// output.
func (s *S3RecordingSink) Write(ctx context.Context, attemptID string, data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[attemptID]
	if !ok {
		session = &recordingSession{startedAt: time.Now()}
		s.sessions[attemptID] = session
	}

	if !session.headerSent {
		header := asciicastHeader{
			Version:   2,
			Width:     80,
			Height:    24,
			Timestamp: session.startedAt.Unix(),
		}
		headerBytes, err := json.Marshal(header)
		if err != nil {
			log.Printf("[telemetry] failed to marshal asciicast header for attempt=%s: %v", attemptID, err)
			return
		}
		session.buf.Write(headerBytes)
		session.buf.WriteByte('\n')
		session.headerSent = true
	}

	elapsed := time.Since(session.startedAt).Seconds()
	// Doc §9.3: "any secret that appears in a learner's environment must
	// be assumed compromised" -- this sink stores raw terminal bytes
	// unredacted, same as LogRecordingSink chose NOT to do for its log
	// line (byte count only). That's a deliberate difference, not an
	// inconsistency: LogRecordingSink's target was application log
	// output (operational visibility, no legitimate reason to see
	// learner secrets there), while THIS sink's entire purpose is
	// producing the actual session recording doc §5.4 asks for --
	// redacting terminal content here would make the recording useless
	// for its real use case (reviewing what a learner actually did).
	// Access control on the resulting S3 object (bucket policy, doc
	// §4.3's retention window) is the real control for this data, not
	// redaction at write time.
	event := []any{elapsed, "o", string(data)}
	eventBytes, err := json.Marshal(event)
	if err != nil {
		log.Printf("[telemetry] failed to marshal asciicast event for attempt=%s: %v", attemptID, err)
		return
	}
	session.buf.Write(eventBytes)
	session.buf.WriteByte('\n')
	session.lastEvent = time.Now()
}

// Run starts the periodic flush loop -- blocks until ctx is cancelled,
// meant to run as a background goroutine for the orchestrator process's
// lifetime (same pattern as internal/reaper.Run).
func (s *S3RecordingSink) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flushAll(context.Background()) // best-effort final flush on shutdown
			return
		case <-ticker.C:
			s.flushAll(ctx)
		}
	}
}

func (s *S3RecordingSink) flushAll(ctx context.Context) {
	s.mu.Lock()
	// Snapshot attempt ids under the lock, then flush each one without
	// holding the lock across a network call -- a slow/hung S3 PUT for
	// one attempt must not block Write() calls for every other
	// concurrent attempt's telemetry tap.
	attemptIDs := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		attemptIDs = append(attemptIDs, id)
	}
	s.mu.Unlock()

	for _, attemptID := range attemptIDs {
		if err := s.flushOne(ctx, attemptID); err != nil {
			log.Printf("[telemetry] S3 recording flush failed for attempt=%s: %v", attemptID, err)
		}
	}
}

func (s *S3RecordingSink) flushOne(ctx context.Context, attemptID string) error {
	s.mu.Lock()
	session, ok := s.sessions[attemptID]
	if !ok || session.buf.Len() == 0 {
		s.mu.Unlock()
		return nil // nothing new since the last flush
	}
	// Copy the buffer contents out, don't reset it -- each flush
	// re-uploads the FULL accumulated recording so far (PutObject
	// overwrite, not append), matching this file's own doc comment on
	// why multipart-append wasn't chosen. Resetting the buffer here
	// would lose everything already flushed if this PutObject call then
	// fails.
	snapshot := make([]byte, session.buf.Len())
	copy(snapshot, session.buf.Bytes())
	s.mu.Unlock()

	key := recordingObjectKey(attemptID)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(snapshot),
		ContentType: aws.String("application/x-asciicast"),
	})
	return err
}

// recordingObjectKey is doc §4.3's retention-table-friendly layout --
// prefixed by attempt id so a lifecycle policy or manual audit can find
// one attempt's recording directly, ".cast" matching asciinema's own
// file extension convention for asciicast files.
func recordingObjectKey(attemptID string) string {
	return fmt.Sprintf("recordings/%s.cast", attemptID)
}

// Forget performs one final synchronous flush of attemptID's buffered
// output, then drops the in-memory buffer -- called once an environment
// is destroyed (doc §4.1: attempt sealed) so this sink's memory usage
// doesn't grow unbounded across the orchestrator process's lifetime for
// attempts that have long since ended, AND so the last flushInterval's
// worth of trailing output isn't silently lost (a real gap in an earlier
// version of this method that just deleted the buffer without flushing
// it first -- caught before shipping, not after). Logs rather than
// returns an error: internal/orchestrator.Destroyer's teardown path
// treats this as best-effort, same as its other post-destroy bookkeeping
// steps (reaper unregister, DB status update) -- a failed final flush
// shouldn't fail the whole environment teardown, since the environment
// itself is already gone by the time this runs.
func (s *S3RecordingSink) Forget(attemptID string) {
	if err := s.flushOne(context.Background(), attemptID); err != nil {
		log.Printf("[telemetry] final flush failed for attempt=%s (recording may be missing trailing output): %v", attemptID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, attemptID)
}
