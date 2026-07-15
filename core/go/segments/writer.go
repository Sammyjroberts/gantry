package segments

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/blob"
	"github.com/Sammyjroberts/gantry/core/go/stream"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/parquet-go/parquet-go"
	"google.golang.org/protobuf/proto"
)

// Rotation defaults. A segment is flushed when its buffered rows reach ~MaxBytes
// of (pre-compression) in-memory size or MaxInterval elapses, whichever first.
// 8MB/60s balances query granularity (smaller = more files to open) against
// write amplification and per-file overhead at bench rates. MaxBytes is an
// in-memory budget; the Zstd-compressed Parquet file on disk is typically far
// smaller.
const (
	DefaultMaxBytes    = 8 << 20 // 8 MiB buffered before a flush
	DefaultMaxInterval = 60 * time.Second
	// bytesPerRowOverhead approximates the fixed per-row in-memory cost (timestamps,
	// numeric columns, slice headers) added to the variable string lengths.
	bytesPerRowOverhead = 48
)

// StreamConn is the read-only slice of *stream.Bus the writer needs: the NATS
// connection, from which it derives its own JetStream context and durable-style
// ordered consumer. This mirrors mcp.BusStreamStater and experiments.Replayer —
// stream.Bus deliberately does not expose a JetStream handle, so consumers that
// need raw messages (the writer needs the batch's received_ns, which
// Bus.Subscribe drops) build their own from Conn().
type StreamConn interface {
	Conn() *nats.Conn
}

// Options configures a Writer. Zero values fall back to the Default* constants.
type Options struct {
	// MaxBytes is the buffered in-memory size that triggers a size-based flush.
	MaxBytes int
	// MaxInterval is the wall-clock period that triggers a time-based flush.
	MaxInterval time.Duration
	// BlobPrefix is the key prefix for segment files ("segments" by default),
	// yielding keys "<prefix>/<device>/<start_ns>-<end_ns>.parquet".
	BlobPrefix string
	// now injects a clock for tests (nil → time.Now).
	now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxInterval <= 0 {
		o.MaxInterval = DefaultMaxInterval
	}
	if o.BlobPrefix == "" {
		o.BlobPrefix = "segments"
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

// Writer drains the telemetry stream into Parquet segments. It maintains a
// per-device row buffer, rotating each device's buffer into its own immutable
// segment file (blob) + catalog row on a size or time threshold. It is the Bench
// flusher; Cloud reuses it against clustered NATS + S3 + Postgres.
type Writer struct {
	conn  StreamConn
	blob  blob.Store
	cat   Catalog
	opts  Options
	newID func() string

	mu       sync.Mutex
	buffers  map[string]*deviceBuffer // device_id -> buffered rows
	lastSeq  uint64                   // highest stream seq observed (buffered or flushed)
	flushSeq uint64                   // highest stream seq durably committed to a segment

	cons   jetstream.ConsumeContext
	ticker *time.Ticker
	stopCh chan struct{}
	wg     sync.WaitGroup
}

type deviceBuffer struct {
	rows     []Row
	bytes    int
	minTs    int64
	maxTs    int64
	maxSeqIn uint64 // highest stream seq contributing to this buffer
}

// NewWriter builds a segment writer over a stream connection, a blob store, and
// a catalog. It does not start consuming until Start is called.
func NewWriter(conn StreamConn, store blob.Store, cat Catalog, opts Options) *Writer {
	return &Writer{
		conn:    conn,
		blob:    store,
		cat:     cat,
		opts:    opts.withDefaults(),
		newID:   defaultID,
		buffers: make(map[string]*deviceBuffer),
		stopCh:  make(chan struct{}),
	}
}

// Start begins draining the stream. It reads the recovery checkpoint from the
// catalog and resumes consuming at high_water_seq + 1 (or from the first
// retained message when the checkpoint is 0), then launches the time-based flush
// loop. Start returns once the consumer is attached; draining continues in the
// background until Stop.
func (w *Writer) Start(ctx context.Context) error {
	js, err := jetstream.New(w.conn.Conn())
	if err != nil {
		return fmt.Errorf("segments: jetstream from conn: %w", err)
	}
	hw, err := w.cat.HighWaterSeq(ctx)
	if err != nil {
		return err
	}
	w.flushSeq = hw
	w.lastSeq = hw

	ccfg := jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{stream.StreamSubject},
	}
	if hw > 0 {
		ccfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		ccfg.OptStartSeq = hw + 1
	} else {
		ccfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}
	cons, err := js.OrderedConsumer(ctx, stream.StreamName, ccfg)
	if err != nil {
		return fmt.Errorf("segments: ordered consumer: %w", err)
	}
	cc, err := cons.Consume(w.onMsg)
	if err != nil {
		return fmt.Errorf("segments: consume: %w", err)
	}
	w.cons = cc

	w.ticker = time.NewTicker(w.opts.MaxInterval)
	w.wg.Add(1)
	go w.flushLoop()
	return nil
}

// onMsg decodes a FrameBatch and appends its frames (stamped with the batch's
// received_ns) to the owning device's buffer. A size threshold triggers an
// inline flush. Corrupt messages are skipped (ordered consumers need no ack).
func (w *Writer) onMsg(msg jetstream.Msg) {
	var fb gantryv1.FrameBatch
	if err := proto.Unmarshal(msg.Data(), &fb); err != nil {
		return
	}
	var seq uint64
	if meta, err := msg.Metadata(); err == nil {
		seq = meta.Sequence.Stream
	}
	receivedNs := int64(fb.ReceivedNs)

	w.mu.Lock()
	if seq > w.lastSeq {
		w.lastSeq = seq
	}
	for _, f := range fb.Frames {
		if f == nil || f.Channel == "" {
			continue
		}
		device := fb.DeviceId
		buf := w.buffers[device]
		if buf == nil {
			buf = &deviceBuffer{minTs: int64(f.TimestampNs), maxTs: int64(f.TimestampNs)}
			w.buffers[device] = buf
		}
		row := rowFromFrame(device, receivedNs, f)
		buf.rows = append(buf.rows, row)
		buf.bytes += bytesPerRowOverhead + len(row.Device) + len(row.Packet) + len(row.Channel) + len(row.VStr)
		if row.TsNs < buf.minTs {
			buf.minTs = row.TsNs
		}
		if row.TsNs > buf.maxTs {
			buf.maxTs = row.TsNs
		}
		if seq > buf.maxSeqIn {
			buf.maxSeqIn = seq
		}
	}
	over := w.bufferedBytesLocked() >= w.opts.MaxBytes
	w.mu.Unlock()

	if over {
		if err := w.Flush(context.Background()); err != nil {
			// A flush failure leaves rows buffered; the next threshold or the time
			// loop retries. Nothing is lost (frames are still in JetStream and
			// un-checkpointed).
			_ = err
		}
	}
}

func (w *Writer) bufferedBytesLocked() int {
	total := 0
	for _, b := range w.buffers {
		total += b.bytes
	}
	return total
}

func (w *Writer) flushLoop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.stopCh:
			return
		case <-w.ticker.C:
			_ = w.Flush(context.Background())
		}
	}
}

// Flush writes every non-empty device buffer to its own segment (blob + catalog)
// and advances the recovery checkpoint. It is safe to call concurrently with
// ingest; buffers are swapped out under the lock so encoding happens off-lock.
func (w *Writer) Flush(ctx context.Context) error {
	w.mu.Lock()
	if len(w.buffers) == 0 {
		w.mu.Unlock()
		return nil
	}
	pending := w.buffers
	w.buffers = make(map[string]*deviceBuffer)
	checkpoint := w.lastSeq
	w.mu.Unlock()

	// Deterministic device order keeps segment ids/keys stable in tests.
	devices := make([]string, 0, len(pending))
	for d := range pending {
		devices = append(devices, d)
	}
	sort.Strings(devices)

	var firstErr error
	for _, device := range devices {
		buf := pending[device]
		if len(buf.rows) == 0 {
			continue
		}
		if err := w.flushDevice(ctx, device, buf, checkpoint); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Re-buffer this device's rows so nothing is dropped; the checkpoint is
			// NOT advanced past un-persisted data because Add carries the checkpoint
			// and only a successful Add commits it.
			w.requeue(device, buf)
		}
	}
	if firstErr == nil {
		w.mu.Lock()
		if checkpoint > w.flushSeq {
			w.flushSeq = checkpoint
		}
		w.mu.Unlock()
	}
	return firstErr
}

// flushDevice encodes one device buffer to Parquet, writes it through the blob
// store, then records it in the catalog (which also advances the checkpoint).
// Order matters: blob first, catalog second. A crash between the two leaves an
// orphan blob that is simply unreferenced (and re-written next run) — the
// documented at-least-once behavior.
func (w *Writer) flushDevice(ctx context.Context, device string, buf *deviceBuffer, checkpoint uint64) error {
	data, err := encodeSegment(buf.rows)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s/%s/%d-%d.parquet", w.opts.BlobPrefix, blobSafe(device), buf.minTs, buf.maxTs)
	if err := w.blob.Put(ctx, key, bytes.NewReader(data)); err != nil {
		return err
	}
	meta := SegmentMeta{
		ID:        w.newID(),
		DeviceID:  device,
		StartNs:   buf.minTs,
		EndNs:     buf.maxTs,
		Frames:    int64(len(buf.rows)),
		Bytes:     int64(len(data)),
		BlobKey:   key,
		CreatedNs: w.opts.now().UnixNano(),
	}
	// The checkpoint advanced by this segment is the max stream seq seen at the
	// flush moment (checkpoint), not just this device's max — all devices in this
	// flush are being committed together, so the whole prefix up to checkpoint is
	// durable once every device Add succeeds. Add's monotonic guard keeps it safe.
	return w.cat.Add(ctx, meta, checkpoint)
}

func (w *Writer) requeue(device string, buf *deviceBuffer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	existing := w.buffers[device]
	if existing == nil {
		w.buffers[device] = buf
		return
	}
	// Prepend the failed rows so time order is preserved.
	merged := append(buf.rows, existing.rows...)
	existing.rows = merged
	existing.bytes += buf.bytes
	if buf.minTs < existing.minTs {
		existing.minTs = buf.minTs
	}
	if buf.maxTs > existing.maxTs {
		existing.maxTs = buf.maxTs
	}
}

// Stop halts consumption, flushes any buffered rows, and releases resources.
func (w *Writer) Stop(ctx context.Context) error {
	if w.cons != nil {
		w.cons.Stop()
	}
	if w.ticker != nil {
		w.ticker.Stop()
	}
	close(w.stopCh)
	w.wg.Wait()
	return w.Flush(ctx)
}

// FlushSeq returns the highest stream sequence durably committed to a segment
// (the recovery checkpoint as this Writer sees it). Exposed for tests.
func (w *Writer) FlushSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushSeq
}

// bufferedRows reports how many rows are currently buffered across all devices.
// Used by tests to wait for the async drain to catch up before flushing.
func (w *Writer) bufferedRows() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, b := range w.buffers {
		n += len(b.rows)
	}
	return n
}

// encodeSegment writes rows to an in-memory Zstd-compressed Parquet file.
func encodeSegment(rows []Row) ([]byte, error) {
	var b bytes.Buffer
	pw := parquet.NewGenericWriter[Row](&b, parquet.Compression(&parquet.Zstd))
	if _, err := pw.Write(rows); err != nil {
		return nil, fmt.Errorf("segments: parquet write: %w", err)
	}
	if err := pw.Close(); err != nil {
		return nil, fmt.Errorf("segments: parquet close: %w", err)
	}
	return b.Bytes(), nil
}
