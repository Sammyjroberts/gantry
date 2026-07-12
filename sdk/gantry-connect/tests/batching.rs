//! Integration tests for the batching/buffer/flush behavior of the SDK core, driven through
//! an in-test recording [`Transport`] (no network).

use std::sync::{Arc, Mutex};
use std::thread::sleep;
use std::time::{Duration, Instant};

use gantry_connect::{Client, FrameBatch, Transport, TransportError};

/// A transport that records every batch it is asked to publish and acks its sequence.
#[derive(Clone, Default)]
struct Recorder {
    batches: Arc<Mutex<Vec<FrameBatch>>>,
}

impl Recorder {
    fn batches(&self) -> Vec<FrameBatch> {
        self.batches.lock().unwrap().clone()
    }
    fn total_frames(&self) -> usize {
        self.batches
            .lock()
            .unwrap()
            .iter()
            .map(|b| b.frames.len())
            .sum()
    }
    fn sequences(&self) -> Vec<u64> {
        self.batches
            .lock()
            .unwrap()
            .iter()
            .map(|b| b.sequence)
            .collect()
    }
}

impl Transport for Recorder {
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
        let seq = batch.sequence;
        self.batches.lock().unwrap().push(batch);
        Ok(seq)
    }
}

fn wait_until(mut f: impl FnMut() -> bool, timeout: Duration) -> bool {
    let start = Instant::now();
    while start.elapsed() < timeout {
        if f() {
            return true;
        }
        sleep(Duration::from_millis(1));
    }
    f()
}

#[test]
fn max_frames_triggers_flush() {
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(5)
        .batch_max_age(Duration::from_secs(3600)) // effectively disable age trigger
        .build()
        .unwrap();

    for i in 0..5 {
        client.send_f64("c", i as f64);
    }

    assert!(
        wait_until(|| rec.total_frames() >= 5, Duration::from_secs(2)),
        "max-frames should have flushed a batch"
    );
    let batches = rec.batches();
    assert_eq!(
        batches[0].frames.len(),
        5,
        "batch flushed exactly at the frame cap"
    );
}

#[test]
fn max_age_triggers_flush() {
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(10_000) // effectively disable frame-count trigger
        .batch_max_age(Duration::from_millis(50))
        .build()
        .unwrap();

    client.send_f64("c", 1.0);
    client.send_f64("c", 2.0);
    client.send_f64("c", 3.0);

    assert!(
        wait_until(|| rec.total_frames() >= 3, Duration::from_secs(2)),
        "max-age should have flushed the partial batch"
    );
    assert_eq!(rec.total_frames(), 3);
}

#[test]
fn sequence_is_monotonic() {
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(2)
        .batch_max_age(Duration::from_secs(3600))
        .build()
        .unwrap();

    for i in 0..6 {
        client.send_f64("c", i as f64);
    }
    client.flush().unwrap();

    let seqs = rec.sequences();
    assert_eq!(seqs, vec![1, 2, 3], "three batches of two, sequences 1..=3");
    assert!(
        seqs.windows(2).all(|w| w[1] > w[0]),
        "sequences strictly increasing"
    );
}

#[test]
fn flush_drains_all_pending() {
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(10_000)
        .batch_max_age(Duration::from_secs(3600))
        .build()
        .unwrap();

    for i in 0..10 {
        client.send_i64("c", i);
    }
    // Nothing should have triggered yet.
    client.flush().unwrap();

    assert_eq!(
        rec.total_frames(),
        10,
        "flush() drains everything synchronously"
    );
    let stats = client.stats();
    assert_eq!(stats.frames_sent, 10);
    assert_eq!(stats.frames_buffered, 0);
}

#[test]
fn buffer_is_bounded_and_drops_oldest() {
    // A recorder transport, but nothing ever triggers a flush (huge frame cap + age), so all
    // frames pile into the bounded buffer and drop-oldest engages deterministically.
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(10_000)
        .batch_max_age(Duration::from_secs(3600))
        .buffer_capacity(4)
        .build()
        .unwrap();

    for i in 0..10 {
        client.send_i64("c", i);
    }

    // Give the flusher a moment; it must NOT have drained (no trigger fired).
    sleep(Duration::from_millis(50));
    let stats = client.stats();
    assert_eq!(stats.frames_enqueued, 10);
    assert_eq!(
        stats.frames_dropped, 6,
        "10 pushed into a cap-4 buffer drops the oldest 6"
    );
    assert_eq!(stats.frames_buffered, 4, "buffer stays bounded at capacity");
    assert_eq!(stats.frames_sent, 0, "flusher never triggered");

    // The surviving frames are the newest four (6,7,8,9); flush and verify.
    client.flush().unwrap();
    let batches = rec.batches();
    let vals: Vec<i64> = batches
        .iter()
        .flat_map(|b| b.frames.iter())
        .map(|f| match f.value.as_ref().unwrap().kind.as_ref().unwrap() {
            gantry_connect::value::Kind::I64(v) => *v,
            other => panic!("unexpected value kind: {other:?}"),
        })
        .collect();
    assert_eq!(vals, vec![6, 7, 8, 9]);
}

#[test]
fn shutdown_flushes_remaining() {
    let rec = Recorder::default();
    let client = Client::builder()
        .device_id("dev")
        .transport(rec.clone())
        .batch_max_frames(10_000)
        .batch_max_age(Duration::from_secs(3600))
        .build()
        .unwrap();

    for i in 0..7 {
        client.send_f64("c", i as f64);
    }
    client.shutdown(); // explicit; Drop would also do this
    assert_eq!(
        rec.total_frames(),
        7,
        "shutdown drains the buffer before joining"
    );
}
