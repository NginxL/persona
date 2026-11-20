//! A single hub group's socket lifecycle. The host supplies a monotonic millisecond clock, decoded
//! input and retry jitter, executes the ordered effects, and returns the exact socket or timer ticket.

mod reconnect;
mod types;

use serde::Serialize;

pub use reconnect::LinkDrop;
use reconnect::{FIRST_BACKOFF, MAX_BACKOFF, REJECTED_RETRY};
pub use types::*;

const GOING_AWAY: u16 = 1001;
const PHASE_DELAY: u64 = 1500;
const WAKE_WINDOW: u64 = 25_000;

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct State {
    pub config: Option<Config>,
    pub socket: Socket,
    pub generation: u64,
    pub cursor: Option<Cursor>,
    pub resumed: bool,
    pub stalled: bool,
    pub rejected: bool,
    pub restart_notice: bool,
    pub backoff_ms: u64,
    pub drop: Option<LinkDrop>,
    pub background: bool,
    pub suspended: bool,
    pub network_up: bool,
    pub shown: Phase,
    pub timer_serial: u64,
    pub timers: Vec<Timer>,
}

impl State {
    fn phase(&self) -> Phase {
        let in_grace = self.drop.is_some_and(|d| !d.grace_over);
        if self.socket == Socket::Idle {
            Phase::Online
        } else if !self.network_up {
            Phase::Waiting
        } else if in_grace || (self.socket == Socket::Connected && !self.stalled) {
            Phase::Online
        } else if self.rejected {
            Phase::Rejected
        } else {
            Phase::Connecting
        }
    }
}

pub struct SyncDriver {
    features: String,
    state: State,
}

impl SyncDriver {
    /// The native client owns its advertised WebSocket features and all credential material.
    pub fn new(features: String) -> Self {
        Self {
            features,
            state: State {
                config: None,
                socket: Socket::Idle,
                generation: 0,
                cursor: None,
                resumed: false,
                stalled: false,
                rejected: false,
                restart_notice: false,
                backoff_ms: FIRST_BACKOFF,
                drop: None,
                background: false,
                suspended: false,
                network_up: true,
                shown: Phase::Online,
                timer_serial: 0,
                timers: Vec::new(),
            },
        }
    }

    pub fn state(&self) -> &State {
        &self.state
    }

    pub fn step(&mut self, now: u64, input: SyncInput) -> Vec<SyncEffect> {
        let mut effects = Vec::new();
        match input {
            SyncInput::Connect { config } => {
                if self.state.config != Some(config) {
                    self.state.cursor = None;
                }
                self.state.config = Some(config);
                self.state.rejected = false;
                self.state.backoff_ms = FIRST_BACKOFF;
                self.open(&mut effects);
            }
            SyncInput::RenewCredential { old, new } => {
                if let Some(config) = &mut self.state.config
                    && config.credential == old
                    && old != new
                {
                    config.credential = new;
                    self.state.rejected = false;
                    if self.state.socket != Socket::Idle {
                        self.state.backoff_ms = FIRST_BACKOFF;
                        self.open(&mut effects);
                    }
                }
            }
            SyncInput::Foreground => {
                self.state.background = false;
                self.state.suspended = false;
                self.ensure_connected(&mut effects);
            }
            SyncInput::Background => {
                self.state.background = true;
                self.suspend(&mut effects);
            }
            SyncInput::WakePush => {
                if self.state.config.is_some() {
                    self.state.suspended = false;
                    self.ensure_connected(&mut effects);
                    self.schedule_callback(TimerKind::Suspend, now.saturating_add(WAKE_WINDOW), &mut effects);
                }
            }
            SyncInput::EnsureConnected => self.ensure_connected(&mut effects),
            SyncInput::ReconnectNow => self.reconnect_now(&mut effects),
            SyncInput::NetworkPath { up } => {
                let was_up = self.state.network_up;
                self.state.network_up = up;
                if up && !was_up {
                    self.reconnect_now(&mut effects);
                } else if up && self.state.socket == Socket::Connected {
                    effects.push(SyncEffect::Ping {
                        generation: self.state.generation,
                    });
                }
            }
            SyncInput::Frame { generation, meta } => {
                if generation == self.state.generation && self.state.socket.has_handle() {
                    self.frame(meta, &mut effects);
                }
            }
            SyncInput::SocketHealth { generation, stalled } => {
                if generation == self.state.generation && self.state.socket == Socket::Connected {
                    self.state.stalled = stalled;
                }
            }
            SyncInput::SocketFailed {
                generation,
                close_code,
                http_status,
                closed_quiet,
                jitter,
            } => {
                if generation == self.state.generation && self.state.socket.has_handle() {
                    let restart = self.state.restart_notice || (!closed_quiet && close_code == Some(GOING_AWAY));
                    self.failed(now, restart, http_status == Some(401), jitter, &mut effects);
                }
            }
            SyncInput::Tick { timer } => self.tick(now, timer, &mut effects),
        }
        self.update_phase(now, &mut effects);
        effects
    }

    fn close_handle(&self, effects: &mut Vec<SyncEffect>) {
        if self.state.socket.has_handle() {
            effects.push(SyncEffect::CloseSocket {
                generation: self.state.generation,
                code: GOING_AWAY,
            });
        }
    }

    fn open(&mut self, effects: &mut Vec<SyncEffect>) {
        let Some(config) = self.state.config else { return };
        if self.state.suspended {
            return;
        }
        self.close_handle(effects);
        self.cancel(TimerKind::Reconnect, effects);
        self.state.generation += 1;
        self.state.socket = Socket::Connecting;
        self.state.stalled = false;
        self.state.resumed = false;
        let mut query = vec![("features".into(), self.features.clone())];
        if let Some(cursor) = &self.state.cursor {
            query.push(("event_stream".into(), cursor.stream.clone()));
            query.push(("event_seq".into(), cursor.seq.to_string()));
        }
        effects.push(SyncEffect::OpenSocket {
            generation: self.state.generation,
            config,
            query,
        });
    }

    fn ensure_connected(&mut self, effects: &mut Vec<SyncEffect>) {
        if self.state.config.is_some() && !self.state.socket.has_handle() {
            self.state.backoff_ms = FIRST_BACKOFF;
            self.open(effects);
        }
    }

    fn reconnect_now(&mut self, effects: &mut Vec<SyncEffect>) {
        if self.state.config.is_some() {
            self.state.backoff_ms = FIRST_BACKOFF;
            if self.state.socket != Socket::Connected || self.state.stalled {
                self.open(effects);
            }
        }
    }

    fn suspend(&mut self, effects: &mut Vec<SyncEffect>) {
        self.state.suspended = true;
        self.close_handle(effects);
        self.cancel(TimerKind::Reconnect, effects);
        self.end_drop(effects);
        self.state.generation += 1;
        self.state.socket = Socket::Idle;
        self.state.stalled = false;
    }

    fn frame(&mut self, meta: FrameMeta, effects: &mut Vec<SyncEffect>) {
        let first = self.state.socket != Socket::Connected;
        if first {
            self.end_drop(effects);
        }
        self.state.note(meta);
        if first {
            let generation = self.state.generation;
            self.state.socket = Socket::Connected;
            self.state.backoff_ms = FIRST_BACKOFF;
            self.state.rejected = false;
            effects.push(SyncEffect::SendWs {
                generation,
                message: Message::ClientHello,
            });
            effects.push(SyncEffect::DidConnect {
                generation,
                refetch: !self.state.resumed,
            });
        }
    }

    fn end_drop(&mut self, effects: &mut Vec<SyncEffect>) {
        self.cancel(TimerKind::Grace, effects);
        self.state.drop = None;
        self.state.restart_notice = false;
    }

    fn failed(&mut self, now: u64, restart: bool, rejected: bool, jitter: Jitter, effects: &mut Vec<SyncEffect>) {
        self.state.restart_notice = false;
        let grace = match &mut self.state.drop {
            None if self.state.socket == Socket::Connected => {
                let d = LinkDrop::new(self.state.generation, now, restart);
                self.state.drop = Some(d);
                Some(d)
            }
            Some(d) if restart && !d.restart => {
                d.restart = true;
                (!d.grace_over).then_some(*d)
            }
            _ => None,
        };
        if let Some(d) = grace {
            self.cancel(TimerKind::Grace, effects);
            self.schedule(
                Timer {
                    kind: TimerKind::Grace,
                    id: d.origin,
                    at: d.grace_until(),
                },
                effects,
            );
        }
        let delay = if let Some(d) = &mut self.state.drop {
            d.retry_delay(now, jitter)
        } else {
            let delay = self.state.backoff_ms;
            self.state.backoff_ms = (delay * 2).min(MAX_BACKOFF);
            delay
        };
        self.state.socket = Socket::Failed;
        self.state.rejected = rejected;
        self.state.stalled = false;
        let delay = if rejected { delay.max(REJECTED_RETRY) } else { delay };
        self.schedule(
            Timer {
                kind: TimerKind::Reconnect,
                id: self.state.generation,
                at: now.saturating_add(delay),
            },
            effects,
        );
    }

    fn schedule(&mut self, timer: Timer, effects: &mut Vec<SyncEffect>) {
        self.state.timers.push(timer);
        effects.push(SyncEffect::Schedule { timer });
    }

    fn schedule_callback(&mut self, kind: TimerKind, at: u64, effects: &mut Vec<SyncEffect>) {
        self.state.timer_serial += 1;
        self.schedule(
            Timer {
                kind,
                id: self.state.timer_serial,
                at,
            },
            effects,
        );
    }

    fn cancel(&mut self, kind: TimerKind, effects: &mut Vec<SyncEffect>) {
        if let Some(index) = self.state.timers.iter().position(|timer| timer.kind == kind) {
            let timer = self.state.timers.remove(index);
            effects.push(SyncEffect::CancelTimer { timer });
        }
    }

    fn tick(&mut self, now: u64, timer: Timer, effects: &mut Vec<SyncEffect>) {
        if now < timer.at {
            return;
        }
        let Some(index) = self.state.timers.iter().position(|t| *t == timer) else {
            return;
        };
        self.state.timers.remove(index);
        match timer.kind {
            TimerKind::Reconnect if timer.id == self.state.generation => self.open(effects),
            TimerKind::Grace => {
                if let Some(d) = &mut self.state.drop
                    && d.origin == timer.id
                {
                    d.grace_over = true;
                }
            }
            TimerKind::Phase => self.show(self.state.phase(), effects),
            TimerKind::Suspend if self.state.background => self.suspend(effects),
            _ => (),
        }
    }

    fn show(&mut self, phase: Phase, effects: &mut Vec<SyncEffect>) {
        if self.state.shown != phase {
            self.state.shown = phase;
            effects.push(SyncEffect::ShowPhase { phase });
        }
    }

    fn update_phase(&mut self, now: u64, effects: &mut Vec<SyncEffect>) {
        let phase = self.state.phase();
        if phase == Phase::Online || self.state.shown != Phase::Online || self.state.drop.is_some_and(|d| d.grace_over)
        {
            self.cancel(TimerKind::Phase, effects);
            self.show(phase, effects);
        } else if !self.state.timers.iter().any(|timer| timer.kind == TimerKind::Phase) {
            self.schedule_callback(TimerKind::Phase, now.saturating_add(PHASE_DELAY), effects);
        }
    }
}
