//! Runs persona-core on one worker thread (docs/plans/plan-rust-core.md, "A deterministic core").
//!
//! - `dispatch` only queues an action's bytes, so a client may call it on its main thread.
//! - The worker applies actions, sends requests to the client and builds each chat's patch.
//! - `changed` wakes the client once per batch of changes; the client then pulls the patch with `drain` on its next
//!   frame, so a burst of pushes reaches the screen as one patch.

pub mod wire;

#[cfg(target_os = "android")]
mod diagnostics;
#[cfg(target_os = "android")]
use diagnostics::{Diagnostics, scheduler_switches};

use std::collections::{HashMap, HashSet};
use std::panic::{AssertUnwindSafe, catch_unwind};
#[cfg(target_os = "android")]
use std::sync::atomic::Ordering;
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
    /// Once-lock means ordinary dispatch/drain only read a null pointer; all instrumentation is opt-in.
    #[cfg(target_os = "android")]
    diagnostics: std::sync::OnceLock<Arc<Diagnostics>>,
    #[cfg(target_os = "android")]
    worker_thread: std::sync::atomic::AtomicU64,
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
        #[cfg(target_os = "android")]
        let sent = if self.shared.diagnostics.get().is_some() {
            let mut sent = Ok(());
            diagnostics::measure_queue(&self.shared, || {
                *diagnostics::timed_lock(&self.shared, &self.shared.pending, false) += 1;
                diagnostics::measure_send(&self.shared, || {
                    sent = self.sender.send(Message::Action(action.to_vec()));
                });
            });
            sent
        } else {
            *lock(&self.shared.pending) += 1;
            self.sender.send(Message::Action(action.to_vec()))
        };
        #[cfg(not(target_os = "android"))]
        let sent = {
            *lock(&self.shared.pending) += 1;
            self.sender.send(Message::Action(action.to_vec()))
        };
        if sent.is_ok() {
            return true;
        }
        self.done(1);
        false
    }

    /// The chat's encoded `wire::ChatPatch` since the last successful drain. Encoding and removal are committed in
    /// that order: a failed encode leaves the pending operations and wake available for the next drain.
    pub fn drain(&self, chat: &str) -> Vec<u8> {
        #[cfg(target_os = "android")]
        let mut patches = diagnostics::timed_lock(&self.shared, &self.shared.patches, true);
        #[cfg(not(target_os = "android"))]
        let mut patches = lock(&self.shared.patches);
        #[cfg(target_os = "android")]
        {
            diagnostics::measure_patch_held(&self.shared, || {
                let started = Instant::now();
                let patch = patches.chats.get(chat).cloned().unwrap_or_default();
                if let Some(diagnostics) = self.shared.diagnostics.get() {
                    diagnostics::add(&diagnostics.counters.patch_clone, started);
                }
                let started = Instant::now();
                let encoded = wire::encode(&patch);
                if let Some(diagnostics) = self.shared.diagnostics.get() {
                    diagnostics::add(&diagnostics.counters.patch_encode, started);
                }
                if let Some(patch) = patches.chats.get_mut(chat) {
                    patch.ops.clear();
                }
                patches.woken.remove(chat);
                encoded
            })
        }
        #[cfg(not(target_os = "android"))]
        {
            let patch = patches.chats.get(chat).cloned().unwrap_or_default();
            let encoded = wire::encode(&patch);
            if let Some(patch) = patches.chats.get_mut(chat) {
                patch.ops.clear();
            }
            patches.woken.remove(chat);
            encoded
        }
    }

    /// Waits until every queued action is applied, up to `timeout`. Zero checks once without waiting. For tests and
    /// shutdown; never on a main thread.
    pub fn flush(&self, timeout: Duration) -> bool {
        let mut pending = lock(&self.shared.pending);
        if timeout.is_zero() {
            return *pending == 0;
        }
        let deadline = Instant::now() + timeout;
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

    /// Same wait as [flush], with opt-in caller timing outside the original benchmark spans.
    #[cfg(target_os = "android")]
    pub fn flush_diagnostic(&self, timeout: Duration) -> bool {
        self.flush(timeout)
    }
    /// Cumulative opt-in timings, off the ordinary dispatch/drain path. Nanosecond values are inclusive spans, so
    /// nested queue and lock diagnostics overlap. Scheduler counters are zero when /proc cannot be read.
    #[cfg(target_os = "android")]
    pub fn diagnostics(&self) -> Vec<i64> {
        let counters = self
            .shared
            .diagnostics
            .get_or_init(|| Arc::new(Diagnostics::default()))
            .counters
            .clone();
        let worker = self.shared.worker_thread.load(Ordering::Relaxed);
        let (worker_voluntary, worker_involuntary) = scheduler_switches(worker);
        let (caller_voluntary, caller_involuntary) = scheduler_switches(0);
        vec![
            counters.pending_lock.load(Ordering::Relaxed) as i64,
            counters.pending_switches.load(Ordering::Relaxed) as i64,
            counters.pending_lock_calls.load(Ordering::Relaxed) as i64,
            counters.pending_contended.load(Ordering::Relaxed) as i64,
            counters.queue.load(Ordering::Relaxed) as i64,
            counters.queue_send.load(Ordering::Relaxed) as i64,
            counters.patch_lock.load(Ordering::Relaxed) as i64,
            counters.patch_switches.load(Ordering::Relaxed) as i64,
            counters.patch_lock_calls.load(Ordering::Relaxed) as i64,
            counters.patch_contended.load(Ordering::Relaxed) as i64,
            counters.patch_lock_without_proc.load(Ordering::Relaxed) as i64,
            counters.patch_held_work.load(Ordering::Relaxed) as i64,
            counters.patch_clone.load(Ordering::Relaxed) as i64,
            counters.patch_encode.load(Ordering::Relaxed) as i64,
            counters.worker_decode_apply.load(Ordering::Relaxed) as i64,
            counters.worker_effect_encode.load(Ordering::Relaxed) as i64,
            counters.worker_host_run.load(Ordering::Relaxed) as i64,
            counters.worker_host_changed.load(Ordering::Relaxed) as i64,
            counters.worker_actions.load(Ordering::Relaxed) as i64,
            worker as i64,
            worker_voluntary as i64,
            worker_involuntary as i64,
            caller_voluntary as i64,
            caller_involuntary as i64,
        ]
    }

    #[cfg(target_os = "android")]
    pub fn enable_diagnostics(&self) {
        self.shared.diagnostics.get_or_init(|| Arc::new(Diagnostics::default()));
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
            // The host's release callback runs on the worker. A final handle released there must not join itself.
            if worker.thread().id() != std::thread::current().id() {
                let _ = worker.join();
            }
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
        #[cfg(target_os = "android")]
        self.shared
            .worker_thread
            .store(diagnostics::thread_id(), Ordering::Relaxed);
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
        #[cfg(target_os = "android")]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            let started = Instant::now();
            let encoded = wire::encode(&Effect::Rejected { reason });
            diagnostics::add(&diagnostics.counters.worker_effect_encode, started);
            let started = Instant::now();
            self.host.run(&encoded);
            diagnostics::add(&diagnostics.counters.worker_host_run, started);
            return;
        }
        self.host.run(&wire::encode(&Effect::Rejected { reason }));
    }

    fn action(&mut self, bytes: &[u8]) {
        #[cfg(target_os = "android")]
        let started = Instant::now();
        #[cfg(target_os = "android")]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            diagnostics::increment(&diagnostics.counters.worker_actions);
        }
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
        #[cfg(target_os = "android")]
        if let Some(diagnostics) = self.shared.diagnostics.get() {
            diagnostics::add(&diagnostics.counters.worker_decode_apply, started);
        }
    }

    fn after_step(&mut self, chat: &str, step: Step) {
        for request in step.requests {
            #[cfg(target_os = "android")]
            if let Some(diagnostics) = self.shared.diagnostics.get() {
                let started = Instant::now();
                let encoded = wire::encode(&Effect::Fetch {
                    chat: chat.to_owned(),
                    request,
                });
                diagnostics::add(&diagnostics.counters.worker_effect_encode, started);
                let started = Instant::now();
                self.host.run(&encoded);
                diagnostics::add(&diagnostics.counters.worker_host_run, started);
                continue;
            }
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
            #[cfg(target_os = "android")]
            if let Some(diagnostics) = self.shared.diagnostics.get() {
                let started = Instant::now();
                self.host.changed(version);
                diagnostics::add(&diagnostics.counters.worker_host_changed, started);
                return;
            }
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
    let overlap = old.len().min(new.len());
    let mut ops = Vec::new();
    let mut paired = vec![false; old.len()];
    for index in common..overlap {
        if old[index].id == new[index].id && old[index].seq == new[index].seq && old[index].part() == new[index].part()
        {
            paired[index] = true;
            if old[index].json() != new[index].json() {
                ops.push(update(index, &new[index]));
            }
        }
    }
    let kept: HashSet<*const Item> = new
        .iter()
        .enumerate()
        .filter(|(index, _)| !paired.get(*index).copied().unwrap_or(false))
        .map(|(_, item)| Arc::as_ptr(item))
        .collect();
    let mut survivors = Vec::new();
    for (index, item) in old.iter().enumerate().rev() {
        if index >= common && !paired[index] && kept.contains(&Arc::as_ptr(item)) {
            survivors.push(item);
        } else if index >= common && !paired[index] {
            ops.push(ListOp::Remove { index: index as u32 });
        }
    }
    survivors.reverse();
    let mut next = 0;
    for (index, item) in new.iter().enumerate().skip(common) {
        if paired.get(index).copied().unwrap_or(false) {
            continue;
        }
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

    #[test]
    fn a_changed_item_is_only_updated_not_removed_and_reinserted() {
        let old = items(r#"[{"id":"1#0","seq":1,"text":"old"},{"id":"2#0","seq":2}]"#);
        let new = items(r#"[{"id":"1#0","seq":1,"text":"new"},{"id":"2#0","seq":2}]"#);
        assert_eq!(diff(&old, &new), vec![update(0, &new[0])]);
        assert_eq!(replay(&old, &diff(&old, &new)), vec!["1#0", "2#0"]);
    }

    #[test]
    fn zero_timeout_is_a_nonblocking_probe() {
        struct NoopHost;
        impl Host for NoopHost {
            fn changed(&self, _version: u64) {}
            fn run(&self, _effect: &[u8]) {}
        }
        let runtime = Runtime::new(Arc::new(NoopHost));
        assert!(runtime.flush(Duration::ZERO));
        let action = wire::encode(&Action::OpenChat {
            chat: "probe".into(),
            snapshot: ChatSnapshot::default(),
        });
        for _ in 0..128 {
            assert!(runtime.dispatch(&action));
        }
        assert!(!runtime.flush(Duration::ZERO));
        assert!(runtime.flush(Duration::from_secs(5)));
    }

    #[test]
    fn worker_release_does_not_join_itself() {
        struct ReleaseHost {
            runtime: Arc<Mutex<Option<Runtime>>>,
        }
        impl Host for ReleaseHost {
            fn changed(&self, _version: u64) {}
            fn run(&self, _effect: &[u8]) {}
        }
        impl Drop for ReleaseHost {
            fn drop(&mut self) {
                self.runtime.lock().unwrap().take();
            }
        }
        let runtime = Arc::new(Mutex::new(None));
        *runtime.lock().unwrap() = Some(Runtime::new(Arc::new(ReleaseHost {
            runtime: runtime.clone(),
        })));
        let guard = runtime.lock().unwrap();
        guard.as_ref().unwrap().dispatch(&wire::encode(&Action::OpenChat {
            chat: "release".into(),
            snapshot: ChatSnapshot::default(),
        }));
        assert!(guard.as_ref().unwrap().flush(Duration::from_secs(5)));
    }
}
