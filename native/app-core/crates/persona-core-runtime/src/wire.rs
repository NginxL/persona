//! What crosses the boundary between the core and a client, encoded with bincode. The clients' codecs are generated
//! from these types by persona-core-typegen; change them and regenerate.

use serde::{Deserialize, Serialize};

pub use persona_core::continuity::Request;

/// How the core is set up. The continuity slice has no timers; reserved for later independently justified domains.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Config {}

/// Into the core, through `dispatch`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Action {
    /// Starts a chat from what the client had cached.
    OpenChat {
        chat: String,
        snapshot: ChatSnapshot,
    },
    CloseChat {
        chat: String,
    },
    Chat {
        chat: String,
        event: ChatEvent,
    },
    /// Continue a chat's immediate fill, without waiting on a timer.
    ContinueFill {
        chat: String,
    },
}

/// A chat's cached state. `items_json` is a JSON array of the hub's items.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ChatSnapshot {
    pub items_json: String,
    pub history: Option<String>,
    pub covered: i64,
    pub has_more: Option<bool>,
    pub loaded_once: bool,
}

/// A batch of items as a response or a push carries it. `items_json` is a JSON array of the hub's items.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct WireBatch {
    pub items_json: String,
    pub history: Option<String>,
    pub from_seq: Option<i64>,
    pub to_seq: Option<i64>,
    pub backlog: bool,
    pub has_more: Option<bool>,
    pub more_after: bool,
}

/// What happened to one chat.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum ChatEvent {
    /// The latest page (always a backlog).
    Latest {
        batch: WireBatch,
    },
    /// A WebSocket push or a subscription's catch-up.
    Pushed {
        batch: WireBatch,
    },
    /// A catch-up page the core asked for (`Request::FetchAfter`).
    After {
        batch: WireBatch,
    },
    /// The latest page fetched instead of filling (`Request::FetchLatest`).
    Reloaded {
        items_json: String,
        history: Option<String>,
        to_seq: Option<i64>,
        has_more: bool,
    },
    /// An older page (`Request::FetchOlder`).
    Older {
        items_json: String,
        has_more: bool,
    },
    /// A page of the opened fold (`Request::FetchRange`).
    Range {
        items_json: String,
        more: bool,
    },
    Reconnected,
    LoadOlder,
    OpenFold {
        fold_id: String,
    },
    /// A catch-up or reload failed or was cancelled.
    FillFailed,
    /// An older page failed or was cancelled.
    OlderFailed,
    /// A fold's page failed or was cancelled.
    RangeFailed,
}

/// Out of the core, through the client's `run` callback.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Effect {
    /// A request for the client to send.
    Fetch { chat: String, request: Request },
    /// An action the core could not use. A bug in the client or the core; never shown to the user as is.
    Rejected { reason: String },
}

/// What changed in one chat since the last `drain`, built by the worker.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ChatPatch {
    /// Grows with every change; the same version means nothing changed.
    pub version: u64,
    pub covered: i64,
    pub history: Option<String>,
    pub has_more: Option<bool>,
    pub loaded_once: bool,
    /// Applied in order to the list the client already has.
    pub ops: Vec<ListOp>,
}

/// One change to a chat's list, keyed so lists keep their identities.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum ListOp {
    Remove {
        index: u32,
    },
    /// `json` is the hub's item exactly as received.
    Insert {
        index: u32,
        id: String,
        json: String,
    },
    /// A repeated id has a new full item, as in iOS's `ChatStore.merge`.
    Update {
        index: u32,
        id: String,
        json: String,
    },
}

pub fn encode<T: Serialize>(value: &T) -> Vec<u8> {
    bincode::serialize(value).expect("wire types always encode")
}

/// Decodes bytes from a client. Lengths inside cannot claim more than the bytes given, so a malformed message
/// cannot make the core allocate beyond its size.
pub fn decode<'a, T: Deserialize<'a>>(bytes: &'a [u8]) -> Result<T, String> {
    use bincode::Options;
    bincode::DefaultOptions::new()
        .with_fixint_encoding()
        .with_limit(bytes.len() as u64)
        .deserialize(bytes)
        .map_err(|e| e.to_string())
}
