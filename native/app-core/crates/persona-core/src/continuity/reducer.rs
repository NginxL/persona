//! One chat's items and how far they are known to be complete (`spec/fixtures/continuity/README.md`).
//!
//! `covered` starts at zero, meaning no baseline. A push that continues from zero sets the baseline. A later push
//! without a baseline waits for the backlog, then asks for a reload once the backlog has arrived. A restored cache does
//! not count as a backlog: only a backlog sets `loaded_once`, and `replace` keeps it as it was. A different history
//! clears items, coverage and pagination before it accepts a page that stands on its own, and asks for a reload for a
//! batch that continues one. A plain item that arrives again keeps its first version; a new or changed fold replaces
//! the work it covers.

use std::collections::HashSet;
use std::sync::Arc;

use crate::item::Item;

/// What a batch means for the chat's continuity.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Gap {
    /// Nothing to fetch.
    None,
    /// Lines are missing after `covered`: fetch them after the settling delay.
    Fill,
    /// The chat cannot be stitched together: fetch the latest page instead.
    Reload,
}

/// The fields of one batch of items, as a response or a push carries them.
#[derive(Debug, Default)]
pub struct Batch {
    pub items: Vec<Item>,
    pub history: Option<String>,
    pub from_seq: Option<i64>,
    pub to_seq: Option<i64>,
    pub backlog: bool,
    pub has_more: Option<bool>,
    pub more_after: bool,
}

#[derive(Debug, Default)]
pub struct Reducer {
    /// Ordered by `(seq, block)`; items with equal keys keep the order they arrived in.
    items: Vec<Arc<Item>>,
    ids: HashSet<String>,
    history: Option<String>,
    covered: i64,
    /// `None` until a page gave usable pagination information, and again after a history reset.
    has_more: Option<bool>,
    loaded_once: bool,
}

impl Reducer {
    pub fn new() -> Self {
        Self::default()
    }

    /// The chat's items in order. Each stays the same `Arc` while it stays in the chat, so a caller can tell what
    /// changed by identity.
    pub fn items(&self) -> &[Arc<Item>] {
        &self.items
    }
    pub fn history(&self) -> Option<&str> {
        self.history.as_deref()
    }
    pub fn covered(&self) -> i64 {
        self.covered
    }
    pub fn has_more(&self) -> Option<bool> {
        self.has_more
    }
    pub fn loaded_once(&self) -> bool {
        self.loaded_once
    }
    pub fn first_seq(&self) -> Option<i64> {
        self.items.first().map(|i| i.seq)
    }
    fn last_seq(&self) -> Option<i64> {
        self.items.last().map(|i| i.seq)
    }

    /// Restores cached state without treating the cache as a received backlog.
    pub fn restore(
        &mut self,
        items: Vec<Item>,
        history: Option<String>,
        covered: i64,
        has_more: Option<bool>,
        loaded_once: bool,
    ) {
        self.clear();
        for item in items {
            self.insert(item);
        }
        self.history = history;
        self.covered = covered.max(0);
        self.has_more = has_more;
        self.loaded_once = loaded_once;
    }

    /// Applies a page or a push and says whether lines must be fetched.
    pub fn apply(&mut self, batch: Batch) -> Gap {
        let Batch {
            items,
            history,
            from_seq,
            to_seq,
            backlog,
            has_more,
            more_after,
        } = batch;
        if let (Some(new), Some(old)) = (&history, &self.history)
            && new != old
        {
            self.restore(Vec::new(), Some(new.clone()), 0, None, false);
            if from_seq.is_some_and(|f| f > 0) {
                return Gap::Reload;
            }
        }
        if history.is_some() {
            self.history = history.clone();
        }
        let Some(to_seq) = to_seq else {
            // Older hubs send no range: merge and move to the last item; a capped batch can only be reloaded.
            self.merge(items, backlog, has_more);
            self.covered = self.covered.max(self.last_seq().unwrap_or(0));
            return if more_after { Gap::Reload } else { Gap::None };
        };
        if let Some(from_seq) = from_seq {
            self.merge(items, backlog, has_more);
            if self.covered == 0 && from_seq > 0 {
                return if self.loaded_once { Gap::Reload } else { Gap::None };
            }
            if from_seq > self.covered {
                return Gap::Fill;
            }
            self.covered = self.covered.max(to_seq);
            return if more_after { Gap::Fill } else { Gap::None };
        }
        if self.covered > 0 && items.first().is_some_and(|first| first.seq > self.covered + 1) {
            // A latest page that does not reach the cache replaces it.
            self.replace(items, history, Some(to_seq), has_more.unwrap_or(true));
            if backlog {
                self.loaded_once = true;
            }
            return Gap::None;
        }
        self.merge(items, backlog, has_more);
        self.covered = self.covered.max(to_seq);
        Gap::None
    }

    /// A reload's latest page. Pushes of the same history strictly past `to_seq` stay, without claiming coverage
    /// through them. `loaded_once` does not change.
    pub fn replace(&mut self, items: Vec<Item>, history: Option<String>, to_seq: Option<i64>, has_more: bool) {
        let same_history = history.is_none() || history == self.history;
        let keep: Vec<Arc<Item>> = match to_seq {
            Some(to) if same_history => self.items.iter().filter(|i| i.seq > to).cloned().collect(),
            _ => Vec::new(),
        };
        self.items.clear();
        self.ids.clear();
        self.has_more = Some(has_more);
        if history.is_some() {
            self.history = history;
        }
        self.merge(items, false, None);
        self.merge_shared(keep);
        self.covered = to_seq.unwrap_or_else(|| self.last_seq().unwrap_or(0));
    }

    /// An older page sets pagination directly, without changing history, coverage or `loaded_once`.
    pub fn older(&mut self, items: Vec<Item>, has_more: Option<bool>) {
        if has_more.is_some() {
            self.has_more = has_more;
        }
        self.merge(items, false, None);
    }

    /// Puts an opened fold's work back in its place, all pages at once, leaving out folds in the response.
    pub fn unfold(&mut self, fold_id: &str, work: Vec<Item>) {
        let Some(index) = self.items.iter().position(|i| i.id == fold_id) else {
            return;
        };
        self.items.remove(index);
        self.ids.remove(fold_id);
        self.merge(work.into_iter().filter(|i| i.kind != "fold").collect(), false, None);
    }

    pub(crate) fn find(&self, id: &str) -> Option<&Arc<Item>> {
        self.items.iter().find(|i| i.id == id)
    }

    fn clear(&mut self) {
        self.items.clear();
        self.ids.clear();
    }

    fn merge(&mut self, new: Vec<Item>, backlog: bool, has_more: Option<bool>) {
        let new_len = new.len();
        let new_first = new.first().map(|i| i.seq);
        let new_min = new.iter().map(|i| i.seq).min();
        let folds: Vec<&Item> = new
            .iter()
            .filter(|n| n.kind == "fold" && !self.find(&n.id).is_some_and(|old| old.same_fold(n)))
            .collect();
        if !folds.is_empty() {
            let ids = &mut self.ids;
            self.items.retain(|item| {
                let covered = item.is_work() && folds.iter().any(|fold| fold.folds(item));
                if covered {
                    ids.remove(&item.id);
                }
                !covered
            });
        }
        // Compared with the first item before this batch, as the clients have always done.
        let existing_first = self.first_seq();
        for item in new {
            self.insert(item);
        }
        let reaches_start = match (new_min, existing_first.or(new_first)) {
            (Some(min), Some(first)) => min <= first,
            _ => false,
        };
        if let Some(more) = has_more
            && backlog
            && (!self.loaded_once || self.items.len() <= new_len)
            && (more || reaches_start)
        {
            self.has_more = Some(more);
        }
        if backlog {
            self.loaded_once = true;
        }
    }

    fn merge_shared(&mut self, items: Vec<Arc<Item>>) {
        for item in items {
            self.insert_shared(item);
        }
    }

    fn insert(&mut self, item: Item) {
        if !self.ids.contains(&item.id) {
            self.insert_shared(Arc::new(item));
        }
    }

    fn insert_shared(&mut self, item: Arc<Item>) {
        if !self.ids.insert(item.id.clone()) {
            return;
        }
        let key = item.key();
        // After every item with an equal key, so equal keys keep their arrival order. Usually the end.
        let index = if self.items.last().is_none_or(|last| last.key() <= key) {
            self.items.len()
        } else {
            self.items.partition_point(|i| i.key() <= key)
        };
        self.items.insert(index, item);
    }
}
