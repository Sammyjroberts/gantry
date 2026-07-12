//! Data-model helpers: channel specs and [`Value`] constructors.
//!
//! `no_std` note: this module depends only on the generated prost types (which need `alloc`,
//! not `std`). Keep it that way — no `std::time`, no `std::sync`, no threads here.

use gantry_connect_proto::v1::{value::Kind, ChannelInfo, Value, ValueKind};

/// Build a `Value` carrying an `f64`.
#[inline]
pub fn value_f64(v: f64) -> Value {
    Value {
        kind: Some(Kind::F64(v)),
    }
}

/// Build a `Value` carrying an `i64`.
#[inline]
pub fn value_i64(v: i64) -> Value {
    Value {
        kind: Some(Kind::I64(v)),
    }
}

/// Build a `Value` carrying a boolean flag.
#[inline]
pub fn value_bool(v: bool) -> Value {
    Value {
        kind: Some(Kind::Flag(v)),
    }
}

/// Build a `Value` carrying UTF-8 text.
#[inline]
pub fn value_text(v: impl Into<String>) -> Value {
    Value {
        kind: Some(Kind::Text(v.into())),
    }
}

/// Build a `Value` carrying raw bytes.
#[inline]
pub fn value_raw(v: impl Into<Vec<u8>>) -> Value {
    Value {
        kind: Some(Kind::Raw(v.into())),
    }
}

/// Channel metadata, registered ahead of or alongside data.
///
/// Maps directly to the wire [`ChannelInfo`]. Convenience constructors set `kind` for you:
/// [`ChannelSpec::f64`], [`ChannelSpec::i64`], [`ChannelSpec::bool`], [`ChannelSpec::text`],
/// [`ChannelSpec::raw`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChannelSpec {
    /// Canonical channel name, e.g. `"drive.motor_left.current_a"`.
    pub name: String,
    /// The kind of value this channel carries.
    pub kind: ValueKind,
    /// Unit string, e.g. `"A"`, `"m/s"`, `"degC"`. Free-form but conventional.
    pub unit: String,
    /// Human-readable description.
    pub description: String,
}

impl ChannelSpec {
    /// Fully-specified constructor.
    pub fn new(
        name: impl Into<String>,
        kind: ValueKind,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self {
            name: name.into(),
            kind,
            unit: unit.into(),
            description: description.into(),
        }
    }

    /// An `f64` channel.
    pub fn f64(
        name: impl Into<String>,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self::new(name, ValueKind::F64, unit, description)
    }

    /// An `i64` channel.
    pub fn i64(
        name: impl Into<String>,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self::new(name, ValueKind::I64, unit, description)
    }

    /// A boolean channel.
    pub fn bool(
        name: impl Into<String>,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self::new(name, ValueKind::Bool, unit, description)
    }

    /// A text channel.
    pub fn text(
        name: impl Into<String>,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self::new(name, ValueKind::Text, unit, description)
    }

    /// A raw-bytes channel.
    pub fn raw(
        name: impl Into<String>,
        unit: impl Into<String>,
        description: impl Into<String>,
    ) -> Self {
        Self::new(name, ValueKind::Raw, unit, description)
    }

    /// Convert to the wire [`ChannelInfo`].
    pub fn to_channel_info(&self) -> ChannelInfo {
        ChannelInfo {
            name: self.name.clone(),
            kind: self.kind as i32,
            unit: self.unit.clone(),
            description: self.description.clone(),
        }
    }
}
