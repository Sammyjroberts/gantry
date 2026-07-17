//! Connect/HTTP transport for the Gantry Edge SDK.
//!
//! Implements [`gantry_edge::Transport`] against the ConnectRPC unary protocol:
//! a plain HTTP `POST` to `<base>/gantry.v1.IngestService/<Method>` with
//! `Content-Type: application/proto` and a body that is the serialized request message.
//! A `200` response body is the serialized response message; any other status carries a
//! Connect error as JSON (`{"code": "...", "message": "..."}`), which we parse best-effort.
//!
//! Blocking I/O via `ureq` — no async runtime (per `docs/ARCHITECTURE.md`, the SDK core and
//! its default transport stay tokio-free).

use std::io::Read;
use std::time::Duration;

use gantry_edge::{Transport, TransportError};
use gantry_edge_proto::prost::Message;
use gantry_edge_proto::v1::{
    ChannelInfo, FrameBatch, PublishBatchRequest, PublishBatchResponse, RegisterChannelsRequest,
};

const INGEST_SERVICE: &str = "gantry.v1.IngestService";
const CONTENT_TYPE_PROTO: &str = "application/proto";

/// A Connect/HTTP transport pointed at a single ingest endpoint (Bench or Cloud).
pub struct HttpTransport {
    base_url: String,
    agent: ureq::Agent,
    /// Optional `Authorization` header value (e.g. `"Bearer gtk_..."`) attached to
    /// every request. Needed to reach a non-loopback Bench, which requires a
    /// scoped token (a loopback Bench trusts you and needs none).
    auth_header: Option<String>,
}

impl HttpTransport {
    /// Create a transport for `base_url` (e.g. `http://localhost:4780`) with default timeouts.
    pub fn new(base_url: impl Into<String>) -> Self {
        Self::builder(base_url).build()
    }

    /// Start building a transport with custom timeouts.
    pub fn builder(base_url: impl Into<String>) -> HttpTransportBuilder {
        HttpTransportBuilder {
            base_url: base_url.into(),
            connect_timeout: Duration::from_secs(5),
            io_timeout: Duration::from_secs(15),
            auth_header: None,
        }
    }

    fn endpoint(&self, method: &str) -> String {
        format!(
            "{}/{}/{}",
            self.base_url.trim_end_matches('/'),
            INGEST_SERVICE,
            method
        )
    }

    /// Perform a Connect unary call: POST proto bytes, return the response body bytes.
    fn unary(&self, method: &str, body: Vec<u8>) -> Result<Vec<u8>, TransportError> {
        let url = self.endpoint(method);
        let mut req = self
            .agent
            .post(&url)
            .set("Content-Type", CONTENT_TYPE_PROTO)
            .set("Accept", CONTENT_TYPE_PROTO)
            .set("Connect-Protocol-Version", "1");
        if let Some(auth) = &self.auth_header {
            req = req.set("Authorization", auth);
        }
        let result = req.send_bytes(&body);

        match result {
            Ok(resp) => {
                let mut buf = Vec::new();
                resp.into_reader()
                    .read_to_end(&mut buf)
                    .map_err(|e| TransportError::Decode(format!("reading response body: {e}")))?;
                Ok(buf)
            }
            Err(ureq::Error::Status(code, resp)) => Err(map_status_error(code, resp)),
            Err(ureq::Error::Transport(t)) => Err(map_transport_error(t)),
        }
    }
}

impl Transport for HttpTransport {
    fn publish(&self, batch: FrameBatch) -> Result<u64, TransportError> {
        let req = PublishBatchRequest { batch: Some(batch) };
        let body = req.encode_to_vec();
        let resp_bytes = self.unary("PublishBatch", body)?;
        let resp = PublishBatchResponse::decode(resp_bytes.as_slice())
            .map_err(|e| TransportError::Decode(format!("PublishBatchResponse: {e}")))?;
        Ok(resp.acked_sequence)
    }

    fn register(&self, device_id: &str, channels: &[ChannelInfo]) -> Result<(), TransportError> {
        let req = RegisterChannelsRequest {
            device_id: device_id.to_string(),
            channels: channels.to_vec(),
        };
        let body = req.encode_to_vec();
        // Response (RegisterChannelsResponse) is empty; we only care that it decoded / 200'd.
        self.unary("RegisterChannels", body)?;
        Ok(())
    }
}

/// Builder for [`HttpTransport`].
pub struct HttpTransportBuilder {
    base_url: String,
    connect_timeout: Duration,
    io_timeout: Duration,
    auth_header: Option<String>,
}

impl HttpTransportBuilder {
    /// Connection-establishment timeout.
    pub fn connect_timeout(mut self, d: Duration) -> Self {
        self.connect_timeout = d;
        self
    }

    /// Per-read/-write timeout.
    pub fn io_timeout(mut self, d: Duration) -> Self {
        self.io_timeout = d;
        self
    }

    /// Attach a bearer token to every request (sent as `Authorization: Bearer <token>`).
    /// Required to publish to a non-loopback Bench; omit for a loopback Bench.
    pub fn bearer_token(mut self, token: impl Into<String>) -> Self {
        self.auth_header = Some(format!("Bearer {}", token.into()));
        self
    }

    /// Set a raw `Authorization` header value verbatim (escape hatch for schemes
    /// other than bearer). Prefer [`bearer_token`](Self::bearer_token).
    pub fn authorization(mut self, value: impl Into<String>) -> Self {
        self.auth_header = Some(value.into());
        self
    }

    /// Build the transport.
    pub fn build(self) -> HttpTransport {
        let agent = ureq::AgentBuilder::new()
            .timeout_connect(self.connect_timeout)
            .timeout_read(self.io_timeout)
            .timeout_write(self.io_timeout)
            .build();
        HttpTransport {
            base_url: self.base_url,
            agent,
            auth_header: self.auth_header,
        }
    }
}

/// Map a non-200 status into a [`TransportError`], parsing a Connect error JSON body if present.
fn map_status_error(code: u16, resp: ureq::Response) -> TransportError {
    let body = resp.into_string().unwrap_or_default();
    if let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) {
        if let Some(connect_code) = value.get("code").and_then(|c| c.as_str()) {
            let message = value
                .get("message")
                .and_then(|m| m.as_str())
                .unwrap_or_default()
                .to_string();
            return TransportError::Protocol {
                code: connect_code.to_string(),
                message,
            };
        }
    }
    TransportError::Status(code)
}

/// Map a ureq transport-level error (no HTTP response) into a [`TransportError`].
fn map_transport_error(t: ureq::Transport) -> TransportError {
    let msg = t.to_string();
    if msg.to_ascii_lowercase().contains("timed out")
        || msg.to_ascii_lowercase().contains("timeout")
    {
        TransportError::Timeout
    } else {
        TransportError::Connection(msg)
    }
}
