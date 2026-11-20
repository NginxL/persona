//! Swift's LinkDrop and EventCursor, after the native transport has classified a frame or failure.

use serde::Serialize;

use super::{Cursor, FrameKind, FrameMeta, Jitter, State};

pub(super) const FIRST_BACKOFF: u64 = 1000;
pub(super) const MAX_BACKOFF: u64 = 20_000;
pub(super) const REJECTED_RETRY: u64 = 60_000;
const RESTART_WINDOW: u64 = 30_000;
const DROP_GRACE: u64 = 8000;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
pub struct LinkDrop {
    pub origin: u64,
    pub since: u64,
    pub restart: bool,
    pub fast_tries: u32,
    pub tries: u32,
    pub grace_over: bool,
}

impl LinkDrop {
    pub(super) fn new(origin: u64, since: u64, restart: bool) -> Self {
        Self {
            origin,
            since,
            restart,
            fast_tries: 0,
            tries: 0,
            grace_over: false,
        }
    }

    pub(super) fn grace_until(&self) -> u64 {
        self.since
            .saturating_add(if self.restart { RESTART_WINDOW } else { DROP_GRACE })
    }

    pub(super) fn retry_delay(&mut self, now: u64, jitter: Jitter) -> u64 {
        let rand = jitter.sample();
        if self.restart && now.saturating_sub(self.since) < RESTART_WINDOW {
            let first = self.fast_tries == 0;
            self.fast_tries = self.fast_tries.saturating_add(1);
            return (if first {
                150.0 + rand * 250.0
            } else {
                500.0 + rand * 500.0
            }) as u64;
        }
        let attempt = self.tries;
        self.tries = self.tries.saturating_add(1);
        if attempt == 0 {
            return (300.0 + rand * 300.0) as u64;
        }
        let base = (FIRST_BACKOFF << (attempt - 1).min(5)).min(MAX_BACKOFF);
        (base as f64 * (0.8 + rand * 0.4)) as u64
    }
}

impl State {
    pub(super) fn note(&mut self, meta: FrameMeta) {
        match meta.kind {
            FrameKind::PrivateExtension => (),
            FrameKind::Hello => {
                let previous = self.cursor.as_ref().map_or("", |cursor| &cursor.stream);
                self.resumed = meta.resumed && meta.stream.as_deref() == Some(previous);
                let seq = meta.seq.unwrap_or(0);
                match (meta.stream.filter(|stream| !stream.is_empty()), &mut self.cursor) {
                    (Some(stream), Some(cursor)) if stream == cursor.stream => {
                        if !self.resumed {
                            cursor.seq = cursor.seq.max(seq);
                        }
                    }
                    (Some(stream), _) => self.cursor = Some(Cursor { stream, seq }),
                    (None, _) => self.cursor = None,
                }
            }
            FrameKind::Restarting | FrameKind::Broadcast => {
                if let (Some(cursor), Some(seq)) = (&mut self.cursor, meta.seq) {
                    cursor.seq = cursor.seq.max(seq);
                }
                if matches!(meta.kind, FrameKind::Restarting) {
                    self.restart_notice = true;
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn swift_jitter_endpoints_and_restart_boundary() {
        // HubRestartTests.aRestartRetriesFastWithoutBackingOff injects 0, .5 and .999.
        for (sample, fast, next, first, second, capped) in [
            (0.0, 150, 500, 300, 800, 16_000),
            (0.5, 275, 750, 450, 1000, 20_000),
            (0.999, 399, 999, 599, 1199, 23_992),
        ] {
            let jitter = Jitter::try_from(sample).unwrap();
            let mut drop = LinkDrop::new(1, 100, true);
            assert_eq!(drop.retry_delay(100, jitter), fast);
            assert_eq!(drop.retry_delay(30_099, jitter), next);
            assert_eq!(drop.retry_delay(30_100, jitter), first);
            assert_eq!(drop.retry_delay(30_101, jitter), second);
            drop.tries = 12;
            assert_eq!(drop.retry_delay(40_000, jitter), capped);
            assert_eq!(drop.fast_tries, 2);
        }
    }
}
