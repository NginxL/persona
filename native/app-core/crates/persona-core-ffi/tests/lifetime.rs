//! The C boundary owns a core until its one free call; concurrent runtime calls may finish or observe closure.

use std::ffi::c_void;
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Barrier};
use std::time::{Duration, Instant};

use persona_core::{
    PersonaCore, PersonaCoreHost, persona_core_bytes_free, persona_core_dispatch, persona_core_drain,
    persona_core_flush, persona_core_free, persona_core_new,
};
use persona_core_runtime::wire::{self, Action, ChatEvent, ChatPatch, ChatSnapshot, Config, WireBatch};

#[derive(Clone, Copy)]
struct SendPointer(*mut PersonaCore);
unsafe impl Send for SendPointer {}
unsafe impl Sync for SendPointer {}

struct Context {
    ready: Sender<bool>,
    release: Sender<()>,
    reenter: Option<SendPointer>,
}

unsafe extern "C" fn changed(context: *mut c_void, _version: u64) {
    let context = unsafe { &*(context as *const Context) };
    let _ = context.ready.send(true);
}

unsafe extern "C" fn release(context: *mut c_void) {
    let context = unsafe { &*(context as *const Context) };
    if let Some(core) = context.reenter {
        // The public dispatch result distinguishes an orderly reentrant close from a deadlock behind its write lock.
        let result = unsafe { persona_core_dispatch(core.0, std::ptr::null(), 0) };
        let _ = context.ready.send(result);
    }
    let _ = context.release.send(());
}

fn host() -> (PersonaCoreHost, Receiver<bool>, Receiver<()>) {
    let (ready_tx, ready_rx) = channel();
    let (release_tx, release_rx) = channel();
    let context = Box::into_raw(Box::new(Context {
        ready: ready_tx,
        release: release_tx,
        reenter: None,
    })) as *mut c_void;
    (
        PersonaCoreHost {
            context,
            changed: Some(changed),
            run: None,
            release: Some(release),
        },
        ready_rx,
        release_rx,
    )
}

fn new_core() -> (*mut PersonaCore, *mut Context, Receiver<bool>, Receiver<()>) {
    let (host, ready, release) = host();
    let context = host.context as *mut Context;
    let config = wire::encode(&Config::default());
    let core = unsafe { persona_core_new(host, config.as_ptr(), config.len()) };
    assert!(!core.is_null());
    // Only the reentrant-close test installs the callback pointer after creating the core.
    (core, context, ready, release)
}

fn open() -> Vec<u8> {
    wire::encode(&Action::OpenChat {
        chat: "chat".into(),
        snapshot: ChatSnapshot::default(),
    })
}

fn reconnect() -> Vec<u8> {
    wire::encode(&Action::Chat {
        chat: "chat".into(),
        event: ChatEvent::Latest {
            batch: WireBatch {
                items_json: r#"[{"id":"1#0","seq":1,"kind":"text","text":"item"}]"#.into(),
                to_seq: Some(1),
                ..WireBatch::default()
            },
        },
    })
}

fn recv_ready(receiver: &Receiver<bool>) {
    receiver
        .recv_timeout(Duration::from_secs(5))
        .expect("worker callback within five seconds");
}

fn free(core: *mut PersonaCore) {
    unsafe { persona_core_free(core) }
}

fn flush_shared(core: SendPointer, timeout_ms: u32) -> bool {
    unsafe { persona_core_flush(core.0, timeout_ms) }
}

fn exercise_until(core: SendPointer, barrier: Arc<Barrier>, action: Vec<u8>, deadline: Instant) {
    barrier.wait();
    let core = core.0;
    while Instant::now() < deadline {
        let _ = unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) };
        let bytes = unsafe { persona_core_drain(core, c"chat".as_ptr() as *const u8, 4) };
        if !bytes.data.is_null() {
            unsafe { persona_core_bytes_free(bytes) };
        }
        let _ = unsafe { persona_core_flush(core, 1) };
    }
}

#[test]
fn close_waits_for_an_in_progress_call_and_releases_the_host_once() {
    let (core, _context, ready, release) = new_core();
    let action = reconnect();
    assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
    assert!(unsafe { persona_core_flush(core, 5_000) });
    let core = SendPointer(core);

    let barrier = Arc::new(Barrier::new(2));
    let caller_barrier = barrier.clone();
    let caller = std::thread::spawn(move || {
        caller_barrier.wait();
        // A close racing this call must not invalidate its runtime reference before it returns.
        flush_shared(core, 5_000)
    });
    barrier.wait();
    free(core.0);
    assert!(caller.join().unwrap());
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("the host is released exactly once");
    assert!(release.try_recv().is_err());
    assert!(!unsafe { persona_core_flush(core.0, 0) });
    assert!(!unsafe { persona_core_dispatch(core.0, std::ptr::null(), 0) });
    let empty = unsafe { persona_core_drain(core.0, std::ptr::null(), 0) };
    assert!(empty.data.is_null());
    unsafe { persona_core_bytes_free(empty) };
    let _ = ready;
}

#[test]
fn live_empty_patch_stays_distinct_from_a_closed_drain() {
    let (core, _context, ready, release) = new_core();
    let action = open();
    assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
    assert!(unsafe { persona_core_flush(core, 5_000) });
    recv_ready(&ready);
    let empty = unsafe { persona_core_drain(core, c"chat".as_ptr() as *const u8, 4) };
    assert!(!empty.data.is_null());
    let patch: ChatPatch = wire::decode(unsafe { std::slice::from_raw_parts(empty.data, empty.length) }).unwrap();
    assert!(patch.ops.is_empty());
    assert!(patch.version > 0);
    unsafe { persona_core_bytes_free(empty) };
    free(core);
    release.recv_timeout(Duration::from_secs(5)).expect("host released");
}

#[test]
fn release_callback_can_call_back_into_the_closed_core() {
    let (core, _context, ready, release) = new_core();
    unsafe { (*_context).reenter = Some(SendPointer(core)) };
    free(core);
    assert!(
        !ready
            .recv_timeout(Duration::from_secs(5))
            .expect("reentrant dispatch observes closed rather than deadlocking")
    );
    release
        .recv_timeout(Duration::from_secs(5))
        .expect("release completes without self-deadlock");
    assert!(release.try_recv().is_err());
}

#[test]
fn concurrent_calls_complete_or_observe_closed_without_touching_freed_runtime() {
    for _ in 0..32 {
        let (core, _context, ready, release) = new_core();
        let action = open();
        assert!(unsafe { persona_core_dispatch(core, action.as_ptr(), action.len()) });
        assert!(unsafe { persona_core_flush(core, 5_000) });
        recv_ready(&ready);
        let core = SendPointer(core);
        let barrier = Arc::new(Barrier::new(4));
        let mut threads = Vec::new();
        for _ in 0..3 {
            let barrier = barrier.clone();
            let action = reconnect();
            threads.push(std::thread::spawn(move || {
                let deadline = Instant::now() + Duration::from_millis(20);
                exercise_until(core, barrier, action, deadline);
            }));
        }
        barrier.wait();
        free(core.0);
        for thread in threads {
            thread.join().unwrap();
        }
        release
            .recv_timeout(Duration::from_secs(5))
            .expect("the racing calls finish before host release");
        assert!(release.try_recv().is_err());
        assert!(!unsafe { persona_core_flush(core.0, 0) });
    }
}
