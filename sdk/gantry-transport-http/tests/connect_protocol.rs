//! Tests the Connect/HTTP transport against a minimal in-test server (`tiny_http`) that
//! speaks just enough of the Connect protocol to validate the request and shape a response.

use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use gantry_connect::{Transport, TransportError};
use gantry_connect_proto::prost::Message;
use gantry_connect_proto::v1::{
    value::Kind, Frame, FrameBatch, PublishBatchRequest, PublishBatchResponse, Value,
};
use gantry_transport_http::HttpTransport;

/// What the test server observed about the request it handled.
struct Observed {
    method_path: String,
    content_type: String,
    body: Vec<u8>,
}

/// Spin a one-shot server that replies to a single request with `status` + `body`, and reports
/// back what it saw. Returns `(base_url, receiver_of_observed)`.
fn spawn_server(status: u16, response_body: Vec<u8>) -> (String, mpsc::Receiver<Observed>) {
    let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
    let port = server.server_addr().to_ip().unwrap().port();
    let (tx, rx) = mpsc::channel();

    thread::spawn(move || {
        if let Some(mut request) = server.incoming_requests().next() {
            let method_path = format!("{} {}", request.method(), request.url());
            let content_type = request
                .headers()
                .iter()
                .find(|h| h.field.equiv("Content-Type"))
                .map(|h| h.value.as_str().to_string())
                .unwrap_or_default();
            let mut body = Vec::new();
            request.as_reader().read_to_end(&mut body).unwrap();

            tx.send(Observed {
                method_path,
                content_type,
                body,
            })
            .unwrap();

            let data = response_body.clone();
            let response = tiny_http::Response::new(
                tiny_http::StatusCode(status),
                vec![tiny_http::Header::from_bytes(
                    &b"Content-Type"[..],
                    if status == 200 {
                        &b"application/proto"[..]
                    } else {
                        &b"application/json"[..]
                    },
                )
                .unwrap()],
                std::io::Cursor::new(data.clone()),
                Some(data.len()),
                None,
            );
            let _ = request.respond(response);
        }
    });

    (format!("http://127.0.0.1:{port}"), rx)
}

fn sample_batch() -> FrameBatch {
    FrameBatch {
        device_id: "sim-robot".into(),
        sequence: 7,
        frames: vec![Frame {
            channel: "drive.motor_left.current_a".into(),
            timestamp_ns: 123,
            value: Some(Value {
                kind: Some(Kind::F64(2.5)),
            }),
        }],
    }
}

#[test]
fn publish_sends_valid_connect_request_and_reads_ack() {
    let resp = PublishBatchResponse { acked_sequence: 7 };
    let (base_url, rx) = spawn_server(200, resp.encode_to_vec());

    let transport = HttpTransport::new(base_url);
    let acked = transport.publish(sample_batch()).expect("publish ok");
    assert_eq!(acked, 7, "transport returns the server's acked_sequence");

    let observed = rx
        .recv_timeout(Duration::from_secs(5))
        .expect("server handled a request");
    assert_eq!(
        observed.method_path, "POST /gantry.v1.IngestService/PublishBatch",
        "correct Connect unary URL"
    );
    assert_eq!(observed.content_type, "application/proto");

    // Body must decode as a PublishBatchRequest carrying our batch.
    let decoded = PublishBatchRequest::decode(observed.body.as_slice()).expect("body decodes");
    let batch = decoded.batch.expect("has batch");
    assert_eq!(batch.device_id, "sim-robot");
    assert_eq!(batch.sequence, 7);
    assert_eq!(batch.frames.len(), 1);
    assert_eq!(batch.frames[0].channel, "drive.motor_left.current_a");
}

#[test]
fn register_hits_the_register_endpoint() {
    let (base_url, rx) = spawn_server(200, Vec::new()); // empty RegisterChannelsResponse
    let transport = HttpTransport::new(base_url);

    transport
        .register(
            "sim-robot",
            &[gantry_connect_proto::v1::ChannelInfo {
                name: "battery.voltage".into(),
                kind: gantry_connect_proto::v1::ValueKind::F64 as i32,
                unit: "V".into(),
                description: "pack voltage".into(),
            }],
        )
        .expect("register ok");

    let observed = rx.recv_timeout(Duration::from_secs(5)).unwrap();
    assert_eq!(
        observed.method_path,
        "POST /gantry.v1.IngestService/RegisterChannels"
    );
}

#[test]
fn connect_error_json_maps_to_protocol_error() {
    let body = br#"{"code":"internal","message":"boom"}"#.to_vec();
    let (base_url, _rx) = spawn_server(500, body);
    let transport = HttpTransport::new(base_url);

    let err = transport
        .publish(sample_batch())
        .expect_err("should be an error");
    match err {
        TransportError::Protocol { code, message } => {
            assert_eq!(code, "internal");
            assert_eq!(message, "boom");
        }
        other => panic!("expected Protocol error, got {other:?}"),
    }
    // "internal" is classified retryable so the flusher would back off and retry.
    assert!(TransportError::Protocol {
        code: "internal".into(),
        message: String::new()
    }
    .is_retryable());
}

#[test]
fn plain_500_without_json_maps_to_status() {
    let (base_url, _rx) = spawn_server(500, b"not json".to_vec());
    let transport = HttpTransport::new(base_url);

    let err = transport.publish(sample_batch()).expect_err("should error");
    assert_eq!(err, TransportError::Status(500));
}

#[test]
fn connection_refused_maps_to_connection_error() {
    // Nothing is listening on this port.
    let transport = HttpTransport::builder("http://127.0.0.1:1")
        .connect_timeout(Duration::from_millis(200))
        .build();
    let err = transport.publish(sample_batch()).expect_err("should error");
    assert!(
        matches!(err, TransportError::Connection(_) | TransportError::Timeout),
        "got {err:?}"
    );
    assert!(err.is_retryable());
}
