//! When a chat asks the hub for lines: immediate catch-up with an in-flight gate, at most eight pages before the
//! latest page instead, reconnect subscriptions, older history and an opened fold's pages. No clocks or transport:
//! the caller runs the requests and reports their results as events.

use serde::{Deserialize, Serialize};

use super::reducer::{Batch, Gap, Reducer};
use crate::item::Item;

/// Catch-up pages before giving up and fetching the latest page.
pub const MAX_FILL_PAGES: u32 = 8;
/// Pages of one opened fold before publishing what arrived.
pub const MAX_RANGE_PAGES: u32 = 100;

/// A request the chat makes.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum Request {
    FetchAfter {
        after_seq: i64,
    },
    FetchLatest {
        fold_work: bool,
    },
    /// `since_seq` only once there is a baseline.
    Subscribe {
        since_seq: Option<i64>,
        fold_work: bool,
    },
    FetchOlder {
        before_seq: i64,
        fold_work: bool,
    },
    /// Both ends included; the next page starts after the last item received.
    FetchRange {
        from_seq: i64,
        through_seq: i64,
    },
}

/// One chat: its items and the requests in flight.
#[derive(Debug, Default)]
pub struct Session {
    reducer: Reducer,
    filling: bool,
    fill_pages: u32,
    /// Gaps and rev moves heard, and how many of them the newest read covers: a read covers what was heard when it
    /// was sent, not when it came back. A move heard while a read is on its way is read again once that read ends.
    heard: u64,
    asked: u64,
    loading_older: bool,
    opening: Option<(String, i64)>,
    range_pages: u32,
    work: Vec<Item>,
}

/// The result of one event: the gap a batch left and the requests it starts.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Step {
    pub gap: Option<Gap>,
    pub requests: Vec<Request>,
}

impl Session {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn reducer(&self) -> &Reducer {
        &self.reducer
    }

    pub fn restore(
        &mut self,
        items: Vec<Item>,
        history: Option<String>,
        covered: i64,
        has_more: Option<bool>,
        loaded_once: bool,
    ) {
        self.reducer.restore(items, history, covered, has_more, loaded_once);
    }

    /// The newest read may miss a line.
    pub fn behind(&self) -> bool {
        self.heard > self.asked
    }

    /// The latest page or a push.
    pub fn received(&mut self, batch: Batch) -> Step {
        let gap = self.reducer.apply(batch);
        Step {
            gap: Some(gap),
            requests: self.settle(gap),
        }
    }

    /// iOS starts a fill as soon as it finds a gap. An already-running fill is not duplicated: its pages re-read
    /// `covered`, and `heard` / `asked` make a move heard in flight get another page when this one ends.
    fn settle(&mut self, gap: Gap) -> Vec<Request> {
        match gap {
            Gap::None => Vec::new(),
            Gap::Fill => {
                self.heard += 1;
                self.continue_fill().requests
            }
            Gap::Reload => vec![Request::FetchLatest { fold_work: true }],
        }
    }

    /// Continues an existing fill, or starts one now from the current `covered`.
    pub fn continue_fill(&mut self) -> Step {
        if self.filling {
            return Step::default();
        }
        self.filling = true;
        self.fill_pages = 0;
        self.asked = self.heard;
        Step {
            gap: None,
            requests: vec![Request::FetchAfter {
                after_seq: self.reducer.covered(),
            }],
        }
    }

    /// A catch-up page. It never carries older-history pagination.
    pub fn after(&mut self, mut batch: Batch) -> Step {
        batch.backlog = false;
        batch.has_more = None;
        let covered_before = self.reducer.covered();
        let to_seq = batch.to_seq;
        let gap = self.reducer.apply(batch);
        self.fill_pages += 1;
        let progressed = self.reducer.covered() > covered_before || to_seq.is_some_and(|to| to > covered_before);
        // A late capped page whose range is already covered must not request the same position again.
        let again = gap == Gap::None && self.behind();
        let requests = if gap == Gap::Reload || (gap == Gap::Fill || again) && self.fill_pages >= MAX_FILL_PAGES {
            vec![Request::FetchLatest { fold_work: true }]
        } else if (gap == Gap::Fill && progressed) || again {
            vec![Request::FetchAfter {
                after_seq: self.reducer.covered(),
            }]
        } else {
            self.finish_fill();
            Vec::new()
        };
        if !requests.is_empty() {
            self.asked = self.heard;
        }
        Step {
            gap: Some(gap),
            requests,
        }
    }

    /// Also called when a catch-up or reload fails or is cancelled, to let the next one start.
    pub fn finish_fill(&mut self) {
        self.filling = false;
        self.fill_pages = 0;
    }

    /// The latest page fetched instead of filling.
    pub fn reload(&mut self, items: Vec<Item>, history: Option<String>, to_seq: Option<i64>, has_more: bool) -> Step {
        self.reducer.replace(items, history, to_seq, has_more);
        self.finish_fill();
        Step::default()
    }

    pub fn reconnect(&mut self) -> Step {
        let covered = self.reducer.covered();
        Step {
            gap: None,
            requests: vec![Request::Subscribe {
                since_seq: (covered > 0).then_some(covered),
                fold_work: true,
            }],
        }
    }

    pub fn load_older(&mut self) -> Step {
        if self.loading_older || self.reducer.has_more() != Some(true) {
            return Step::default();
        }
        let Some(first) = self.reducer.first_seq() else {
            return Step::default();
        };
        self.loading_older = true;
        Step {
            gap: None,
            requests: vec![Request::FetchOlder {
                before_seq: first,
                fold_work: true,
            }],
        }
    }

    pub fn older(&mut self, items: Vec<Item>, has_more: bool) -> Step {
        self.reducer.older(items, Some(has_more));
        self.finish_older();
        Step::default()
    }

    /// Also called when an older page fails or is cancelled.
    pub fn finish_older(&mut self) {
        self.loading_older = false;
    }

    /// Opens a fold: one at a time.
    pub fn open_fold(&mut self, id: &str) -> Step {
        if self.opening.is_some() {
            return Step::default();
        }
        let Some(item) = self.reducer.find(id).filter(|i| i.kind == "fold") else {
            return Step::default();
        };
        let Some(range) = &item.fold else {
            return Step::default();
        };
        let (seq, through) = (item.seq, range.through_seq);
        self.opening = Some((item.id.clone(), through));
        self.range_pages = 0;
        self.work.clear();
        Step {
            gap: None,
            requests: vec![Request::FetchRange {
                from_seq: seq,
                through_seq: through,
            }],
        }
    }

    /// A page of the opened fold. The work is published once, when the last page arrives.
    pub fn range(&mut self, items: Vec<Item>, more: bool) -> Step {
        let Some((fold_id, through)) = self.opening.clone() else {
            return Step::default();
        };
        let last = items.last().map(|i| i.seq);
        self.work.extend(items);
        self.range_pages += 1;
        if more
            && let Some(last) = last
            && self.range_pages < MAX_RANGE_PAGES
        {
            return Step {
                gap: None,
                requests: vec![Request::FetchRange {
                    from_seq: last + 1,
                    through_seq: through,
                }],
            };
        }
        let work = std::mem::take(&mut self.work);
        self.reducer.unfold(&fold_id, work);
        self.cancel_fold();
        Step::default()
    }

    /// Drops unpublished fold pages after a failure or cancellation.
    pub fn cancel_fold(&mut self) {
        self.opening = None;
        self.range_pages = 0;
        self.work.clear();
    }
}
