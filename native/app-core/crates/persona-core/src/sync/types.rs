//! Inputs are already decoded. Config and credential numbers are opaque host handles.

use serde::{Deserialize, Serialize};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub hub: u64,
    pub credential: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Cursor {
    pub stream: String,
    pub seq: i64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Socket {
    Idle,
    Connecting,
    Connected,
    Failed,
}

impl Socket {
    pub(super) fn has_handle(self) -> bool {
        matches!(self, Self::Connecting | Self::Connected)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Phase {
    Online,
    Waiting,
    Connecting,
    Rejected,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TimerKind {
    Reconnect,
    Grace,
    Phase,
    Suspend,
}

/// Echo the whole ticket on delivery. `id` is the socket generation (reconnect), the generation that
/// started the drop (grace), or a unique callback number (phase and silent-push suspension).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Timer {
    pub kind: TimerKind,
    pub id: u64,
    pub at: u64,
}

/// The host supplies the same uniform [0, 1) sample as Swift's LinkDrop.retryDelay.
#[derive(Clone, Copy, Debug, PartialEq, Deserialize)]
#[serde(try_from = "f64")]
pub struct Jitter(f64);

impl TryFrom<f64> for Jitter {
    type Error = &'static str;

    fn try_from(value: f64) -> Result<Self, Self::Error> {
        if (0.0..1.0).contains(&value) {
            Ok(Self(value))
        } else {
            Err("retry jitter must be in [0, 1)")
        }
    }
}

impl Jitter {
    pub(super) fn sample(self) -> f64 {
        self.0
    }
}

#[derive(Clone, Copy, Debug, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum FrameKind {
    Hello,
    Broadcast,
    Restarting,
    PrivateExtension,
}

/// Only connection metadata. The host still owns raw frame decoding and ordinary payload dispatch.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FrameMeta {
    pub kind: FrameKind,
    pub stream: Option<String>,
    pub seq: Option<i64>,
    #[serde(default)]
    pub resumed: bool,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum SyncInput {
    Connect {
        config: Config,
    },
    RenewCredential {
        old: u64,
        new: u64,
    },
    Foreground,
    Background,
    WakePush,
    EnsureConnected,
    ReconnectNow,
    NetworkPath {
        up: bool,
    },
    Frame {
        generation: u64,
        meta: FrameMeta,
    },
    /// The host's current heartbeat result. Heartbeat scheduling itself is a later slice.
    SocketHealth {
        generation: u64,
        stalled: bool,
    },
    SocketFailed {
        generation: u64,
        close_code: Option<u16>,
        http_status: Option<u16>,
        #[serde(default)]
        closed_quiet: bool,
        jitter: Jitter,
    },
    Tick {
        timer: Timer,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Message {
    ClientHello,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum SyncEffect {
    OpenSocket {
        generation: u64,
        config: Config,
        query: Vec<(String, String)>,
    },
    CloseSocket {
        generation: u64,
        code: u16,
    },
    SendWs {
        generation: u64,
        message: Message,
    },
    /// LiveConnection.onConnected: a full hello needs the host's refresh; a resumed one uses replay.
    DidConnect {
        generation: u64,
        refetch: bool,
    },
    Ping {
        generation: u64,
    },
    Schedule {
        timer: Timer,
    },
    CancelTimer {
        timer: Timer,
    },
    ShowPhase {
        phase: Phase,
    },
}
