//! Runs persona-core on one worker thread (docs/plans/plan-rust-core.md, "A deterministic core").
//!
//! - `dispatch` only queues an action's bytes, so a client may call it on its main thread.
//! - The worker applies actions, sends requests to the client and builds each chat's patch.
//! - `changed` wakes the client once per batch of changes; the client then pulls the patch with `drain` on its next
//!   frame, so a burst of pushes reaches the screen as one patch.

pub mod wire;

use std::collections::{HashMap, HashSet};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use persona_core::Item;
use persona_core::continuity::{Batch, Session, Step};
use wire::{Action, ChatEvent, ChatPatch, ChatSnapshot, Effect, ListOp, WireBatch};

/// The client's side: called on the core's worker thread, so implementations must be thread-safe and quick.
pub trait Host: Send + Sync + 'static {
    /// Something changed at `version`: pull patches on the next frame.
    fn changed(&self, version: u64);
    /// An encoded `wire::Effect` for the client to run.
    fn run(&self, effect: &[u8]);
}

enum Message {
    Action(Vec<u8>),
    Stop,
}

#[derive(Default)]
struct Shared {
    patches: Mutex<Patches>,
    /// Actions queued and not yet applied, for `flush`.
    pending: Mutex<u64>,
    idle: Condvar,
}

#[derive(Default)]
struct Patches {
    version: u64,
    chats: HashMap<String, ChatPatch>,
    /// Chats whose latest changes have not been drained. One chat's drain cannot consume another's wake-up.
    woken: HashSet<String>,
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}

pub struct Runtime {
    sender: Sender<Message>,
    shared: Arc<Shared>,
    worker: Option<JoinHandle<()>>,
}

impl Runtime {
    pub fn new(host: Arc<dyn Host>) -> Runtime {
        let (sender, receiver) = channel();
        let shared = Arc::new(Shared::default());
        let worker_shared = shared.clone();
        let worker = std::thread::Builder::new()
            .name("persona-core".into())
            .spawn(move || Worker::new(host, worker_shared).run(receiver))
            .expect("start the core's worker thread");
        Runtime {
            sender,
            shared,
            worker: Some(worker),
        }
    }

    /// Queues an encoded `wire::Action`. Never blocks on the worker.
    pub fn dispatch(&self, action: &[u8]) -> bool {
        *lock(&self.shared.pending) += 1;
        if self.sender.send(Message::Action(action.to_vec())).is_ok() {
            return true;
        }
        self.done(1);
        false
    }

    /// The chat's encoded `wire::ChatPatch` since the last drain: the latest state and the list changes. Taking it
    /// is a swap under a lock; the worker already built it.
    pub fn drain(&self, chat: &str) -> Vec<u8> {
        let patch = {
            let mut patches = lock(&self.shared.patches);
            patches.woken.remove(chat);
            match patches.chats.get_mut(chat) {
                Some(patch) => {
                    let ops = std::mem::take(&mut patch.ops);
                    ChatPatch { ops, ..patch.clone() }
                }
                None => ChatPatch::default(),
            }
        };
        wire::encode(&patch)
    }

    /// Waits until every queued action is applied, up to `timeout`. For tests and shutdown; never on a main thread.
    pub fn flush(&self, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        let mut pending = lock(&self.shared.pending);
        while *pending > 0 {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return false;
            }
            pending = self
                .shared
                .idle
                .wait_timeout(pending, left)
                .unwrap_or_else(PoisonError::into_inner)
                .0;
        }
        true
    }

    fn done(&self, count: u64) {
        let mut pending = lock(&self.shared.pending);
        *pending = pending.saturating_sub(count);
        if *pending == 0 {
            self.shared.idle.notify_all();
        }
    }
}

impl Drop for Runtime {
    fn drop(&mut self) {
        let _ = self.sender.send(Message::Stop);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

struct Chat {
    session: Session,
    /// The list as the client last received it, to diff against.
    published: Vec<Arc<Item>>,
}

struct Worker {
    host: Arc<dyn Host>,
    shared: Arc<Shared>,
    chats: HashMap<String, Chat>,
}

impl Worker {
    fn new(host: Arc<dyn Host>, shared: Arc<Shared>) -> Worker {
        Worker {
            host,
            shared,
            chats: HashMap::new(),
        }
    }

    fn run(mut self, receiver: Receiver<Message>) {
        loop {
            match receiver.recv() {
                Ok(Message::Stop) | Err(_) => return,
                Ok(Message::Action(bytes)) => {
                    self.guarded(|worker| worker.action(&bytes));
                    let mut pending = lock(&self.shared.pending);
                    *pending = pending.saturating_sub(1);
                    if *pending == 0 {
                        self.shared.idle.notify_all();
                    }
                }
            }
        }
    }

    /// A panic in one action is reported and the worker goes on with the next.
    fn guarded(&mut self, work: impl FnOnce(&mut Worker)) {
        if catch_unwind(AssertUnwindSafe(|| work(self))).is_err() {
            self.reject("the core failed on an action".into());
        }
    }

    fn reject(&self, reason: String) {
        self.host.run(&wire::encode(&Effect::Rejected { reason }));
    }

    fn action(&mut self, bytes: &[u8]) {
        let action: Action = match wire::decode(bytes) {
            Ok(action) => action,
            Err(error) => return self.reject(format!("undecodable action: {error}")),
        };
        match action {
            Action::OpenChat { chat, snapshot } => {
                let ChatSnapshot {
                    items_json,
                    history,
                    covered,
                    has_more,
                    loaded_once,
                } = snapshot;
                let items = match items_from(&items_json) {
                    Ok(items) => items,
                    Err(error) => return self.reject(error),
                };
                let mut session = Session::new();
                session.restore(items, history, covered, has_more, loaded_once);
                self.chats.insert(
                    chat.clone(),
                    Chat {
                        session,
                        published: Vec::new(),
                    },
                );
                self.publish(&chat);
            }
            Action::CloseChat { chat } => {
                self.chats.remove(&chat);
                let mut patches = lock(&self.shared.patches);
                patches.chats.remove(&chat);
                patches.woken.remove(&chat);
            }
            Action::Chat { chat, event } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject(format!("event for a chat that is not open: {chat}"));
                };
                let step = match apply(&mut state.session, event) {
                    Ok(step) => step,
                    Err(error) => return self.reject(error),
                };
                self.after_step(&chat, step);
            }
            Action::ContinueFill { chat } => {
                let Some(state) = self.chats.get_mut(&chat) else {
                    return self.reject(format!("fill for a chat that is not open: {chat}"));
                };
                let step = state.session.continue_fill();
                self.after_step(&chat, step);
            }
        }
    }

    fn after_step(&mut self, chat: &str, step: Step) {
        for request in step.requests {
            self.host.run(&wire::encode(&Effect::Fetch {
                chat: chat.to_owned(),
                request,
            }));
        }
        self.publish(chat);
    }

    /// Diffs the chat against what the client has and adds the changes to its pending patch.
    fn publish(&mut self, chat: &str) {
        let Some(state) = self.chats.get_mut(chat) else { return };
        let reducer = state.session.reducer();
        let current = reducer.items();
        let ops = diff(&state.published, current);
        let items_changed = !ops.is_empty();
        if items_changed {
            state.published = current.to_vec();
        }
        let wake = {
            let mut patches = lock(&self.shared.patches);
            let patch = patches.chats.entry(chat.to_owned()).or_default();
            let state_changed = patch.covered != reducer.covered()
                || patch.history.as_deref() != reducer.history()
                || patch.has_more != reducer.has_more()
                || patch.loaded_once != reducer.loaded_once();
            if !items_changed && !state_changed && patch.version != 0 {
                None
            } else {
                patch.ops.extend(ops);
                patch.covered = reducer.covered();
                patch.history = reducer.history().map(str::to_owned);
                patch.has_more = reducer.has_more();
                patch.loaded_once = reducer.loaded_once();
                patches.version += 1;
                let version = patches.version;
                patches.chats.get_mut(chat).expect("patch inserted above").version = version;
                // Every real change wakes this chat, even if a previous frame's patch is still pending. The client
                // coalesces at its frame boundary; another chat's drain cannot consume this chat's notification.
                Some(version)
            }
        };
        if let Some(version) = wake {
            self.host.changed(version);
        }
    }
}

fn items_from(json: &str) -> Result<Vec<Item>, String> {
    if json.is_empty() {
        return Ok(Vec::new());
    }
    Item::list_from_json(json).map_err(|e| e.to_string())
}

fn batch(wire: WireBatch, backlog: bool) -> Result<Batch, String> {
    Ok(Batch {
        items: items_from(&wire.items_json)?,
        history: wire.history,
        from_seq: wire.from_seq,
        to_seq: wire.to_seq,
        backlog,
        has_more: wire.has_more,
        more_after: wire.more_after,
    })
}

fn apply(session: &mut Session, event: ChatEvent) -> Result<Step, String> {
    Ok(match event {
        ChatEvent::Latest { batch: b } => session.received(batch(b, true)?),
        ChatEvent::Pushed { batch: b } => {
            let backlog = b.backlog;
            session.received(batch(b, backlog)?)
        }
        ChatEvent::After { batch: b } => session.after(batch(b, false)?),
        ChatEvent::Reloaded {
            items_json,
            history,
            to_seq,
            has_more,
        } => session.reload(items_from(&items_json)?, history, to_seq, has_more),
        ChatEvent::Older { items_json, has_more } => session.older(items_from(&items_json)?, has_more),
        ChatEvent::Range { items_json, more } => session.range(items_from(&items_json)?, more),
        ChatEvent::Reconnected => session.reconnect(),
        ChatEvent::LoadOlder => session.load_older(),
        ChatEvent::OpenFold { fold_id } => session.open_fold(&fold_id),
        ChatEvent::FillFailed => {
            session.finish_fill();
            Step::default()
        }
        ChatEvent::OlderFailed => {
            session.finish_older();
            Step::default()
        }
        ChatEvent::RangeFailed => {
            session.cancel_fold();
            Step::default()
        }
    })
}

/// The list operations that turn `old` into `new`. Identical items retain their Arc. Removals go from the end;
/// a changed item with the same position and id is an Update, preserving its native list identity.
fn diff(old: &[Arc<Item>], new: &[Arc<Item>]) -> Vec<ListOp> {
    let common = old.iter().zip(new).take_while(|(a, b)| Arc::ptr_eq(a, b)).count();
    let mut ops = Vec::new();
    for index in common..old.len().min(new.len()) {
        if old[index].id == new[index].id && old[index].seq == new[index].seq && old[index].part() == new[index].part()
        {
            ops.push(update(index, &new[index]));
        }
    }
    let kept: HashSet<*const Item> = new.iter().skip(common).map(Arc::as_ptr).collect();
    let mut survivors = Vec::new();
    for (index, item) in old.iter().enumerate().rev() {
        if index >= common && kept.contains(&Arc::as_ptr(item)) {
            survivors.push(item);
        } else if index >= common {
            ops.push(ListOp::Remove { index: index as u32 });
        }
    }
    survivors.reverse();
    let mut next = 0;
    for (index, item) in new.iter().enumerate().skip(common) {
        if next < survivors.len() && Arc::ptr_eq(survivors[next], item) {
            next += 1;
        } else {
            ops.push(insert(index, item));
        }
    }
    if next == survivors.len() {
        return ops;
    }
    // Never expected: items that stayed changed order. Start the list over.
    let mut ops: Vec<ListOp> = (0..old.len())
        .rev()
        .map(|index| ListOp::Remove { index: index as u32 })
        .collect();
    ops.extend(new.iter().enumerate().map(|(i, item)| insert(i, item)));
    ops
}

fn insert(index: usize, item: &Item) -> ListOp {
    ListOp::Insert {
        index: index as u32,
        id: item.id.clone(),
        json: item.json().to_owned(),
    }
}

fn update(index: usize, item: &Item) -> ListOp {
    ListOp::Update {
        index: index as u32,
        id: item.id.clone(),
        json: item.json().to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn items(json: &str) -> Vec<Arc<Item>> {
        Item::list_from_json(json).unwrap().into_iter().map(Arc::new).collect()
    }

    fn replay(old: &[Arc<Item>], ops: &[ListOp]) -> Vec<String> {
        let mut list: Vec<String> = old.iter().map(|i| i.id.clone()).collect();
        for op in ops {
            match op {
                ListOp::Remove { index } => {
                    list.remove(*index as usize);
                }
                ListOp::Insert { index, id, .. } => list.insert(*index as usize, id.clone()),
                ListOp::Update { index, id, .. } => list[*index as usize] = id.clone(),
            }
        }
        list
    }

    #[test]
    fn diff_appends_removes_and_inserts() {
        let old = items(r#"[{"id":"1#0","seq":1},{"id":"2#0","seq":2},{"id":"3#0","seq":3}]"#);
        let extra = items(r#"[{"id":"4#0","seq":4},{"id":"0#0","seq":0}]"#);
        let appended = vec![old[0].clone(), old[1].clone(), old[2].clone(), extra[0].clone()];
        assert_eq!(diff(&old, &appended), vec![insert(3, &extra[0])]);
        let mixed = vec![extra[1].clone(), old[0].clone(), old[2].clone(), extra[0].clone()];
        let ops = diff(&old, &mixed);
        assert_eq!(replay(&old, &ops), vec!["0#0", "1#0", "3#0", "4#0"]);
        assert!(diff(&old, &old).is_empty());
    }
}
