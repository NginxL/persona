use std::path::PathBuf;

use persona_core::sync::{Config, Jitter, SyncDriver, SyncEffect, SyncInput, TimerKind};
use serde::Deserialize;
use serde_json::{Value, json};

#[derive(Deserialize)]
struct Fixture {
    version: u32,
    cases: Vec<Case>,
}

#[derive(Clone, Deserialize)]
struct Case {
    name: String,
    description: String,
    source: String,
    from_test: Option<String>,
    #[serde(default)]
    derived: bool,
    input: Scenario,
    expected: Vec<Value>,
}

#[derive(Clone, Deserialize)]
struct Scenario {
    features: String,
    initial: Value,
    events: Vec<Value>,
}

#[derive(Deserialize)]
struct Event {
    now: u64,
    #[serde(flatten)]
    input: SyncInput,
}

fn fixture(topic: &str) -> Fixture {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../spec/fixtures/sync-driver")
        .join(format!("{topic}.json"));
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn replay(case: &Case) -> Result<(), String> {
    let mut driver = SyncDriver::new(case.input.features.clone());
    let initial = serde_json::to_value(driver.state()).unwrap();
    if case.input.initial != initial || case.input.events.len() != case.expected.len() {
        return Err(format!("{}: initial state or number of steps", case.name));
    }
    for (index, (input, expected)) in case.input.events.iter().zip(&case.expected).enumerate() {
        let event: Event = serde_json::from_value(input.clone()).map_err(|error| error.to_string())?;
        let actions = driver.step(event.now, event.input);
        let actual = json!({"state": driver.state(), "actions": actions});
        if actual != *expected {
            return Err(format!(
                "{} step {}: {input}\nexpected {expected}\n  actual {actual}",
                case.name,
                index + 1
            ));
        }
    }
    Ok(())
}

fn check(topic: &str, manifest: &[(&str, usize)]) {
    let corpus = fixture(topic);
    assert_eq!(corpus.version, 1);
    let actual: Vec<_> = corpus
        .cases
        .iter()
        .map(|case| (case.name.as_str(), case.input.events.len()))
        .collect();
    assert_eq!(actual, manifest);
    for case in &corpus.cases {
        assert!(!case.description.is_empty() && case.source.contains(".swift"));
        assert_ne!(
            case.derived,
            case.from_test.as_ref().is_some_and(|test| test.contains(".swift"))
        );
        replay(case).unwrap();
    }
}

#[test]
fn reconnect_backoff() {
    check(
        "reconnect_backoff",
        &[
            ("ordinary_and_rejected", 17),
            ("ordinary_drop", 13),
            ("restart_fast_window", 12),
            ("restart_extends_an_existing_drop", 9),
        ],
    );
}

#[test]
fn event_cursor() {
    check(
        "event_cursor",
        &[
            ("resumed_stream_and_replay", 11),
            ("new_stream_full_and_unnumbered", 10),
            ("config_and_credential", 11),
        ],
    );
}

#[test]
fn lifecycle() {
    check(
        "lifecycle",
        &[
            ("two_background_wakes", 11),
            ("foreground_at_wake_deadline", 9),
            ("path_change_and_stalled_socket", 17),
        ],
    );
}

#[test]
fn rejects_invalid_inputs() {
    for bad in [
        json!({"kind":"socket_failed", "generation":1, "jitter":-0.1}),
        json!({"kind":"socket_failed", "generation":1, "jitter":1}),
        json!({"kind":"socket_failed", "generation":1}),
        json!({"kind":"frame", "meta":{"kind":"broadcast"}}),
        json!({"kind":"tick", "timer":{"id":{"reconnect":"old"},"at":1000}}),
        json!({"kind":"frame", "generation":1,"meta":{"kind":"hello", "seq":"2"}}),
        json!({"kind":"invented"}),
    ] {
        assert!(serde_json::from_value::<SyncInput>(bad.clone()).is_err(), "{bad}");
    }
}

#[test]
fn replay_detects_changed_state_action_order_and_missing_steps() {
    let original = fixture("event_cursor").cases.remove(0);
    replay(&original).unwrap();
    let mut changed = original.clone();
    changed.expected[0]["state"]["generation"] = json!(0);
    assert!(replay(&changed).is_err());
    let mut reordered = original.clone();
    let effects = reordered.expected[0]["actions"].as_array_mut().unwrap();
    assert!(effects.len() >= 2);
    effects.swap(0, 1);
    assert!(replay(&reordered).is_err());
    let mut missing = original;
    missing.expected.pop();
    assert!(replay(&missing).is_err());

    let mut backoff = fixture("reconnect_backoff").cases.remove(0);
    backoff.expected[1]["actions"][0]["timer"]["at"] = json!(500);
    assert!(replay(&backoff).is_err());
    let mut wakes = fixture("lifecycle").cases.remove(0);
    wakes.expected[5]["state"]["timers"].as_array_mut().unwrap().remove(0);
    assert!(replay(&wakes).is_err());
}

#[test]
fn immediate_retry_supersedes_the_exact_pending_ticket() {
    let mut driver = SyncDriver::new("v1".into());
    driver.step(
        0,
        SyncInput::Connect {
            config: Config { hub: 1, credential: 1 },
        },
    );
    let fail = |generation| SyncInput::SocketFailed {
        generation,
        close_code: None,
        http_status: Some(401),
        closed_quiet: false,
        jitter: Jitter::try_from(0.0).unwrap(),
    };
    let denied = driver.step(20, fail(1));
    let old = denied
        .iter()
        .find_map(|effect| match effect {
            SyncEffect::Schedule { timer } if timer.kind == TimerKind::Reconnect => Some(*timer),
            _ => None,
        })
        .unwrap();
    assert_eq!(old.at, 60_020);
    assert!(driver.step(30, fail(1)).is_empty()); // the failed handle has already closed
    let reopen = driver.step(90, SyncInput::ReconnectNow);
    assert!(matches!(reopen.as_slice(), [
        SyncEffect::CancelTimer {timer}, SyncEffect::OpenSocket {generation: 2, ..},
    ] if *timer == old));
    assert_eq!(driver.state().backoff_ms, 1000);
    let before = driver.state().clone();
    assert!(driver.step(old.at, SyncInput::Tick { timer: old }).is_empty());
    assert!(driver.step(old.at + 1, fail(1)).is_empty());
    assert_eq!(&before, driver.state());
}

#[test]
fn jitter_and_clock_edges() {
    for sample in [f64::NAN, f64::INFINITY, -0.01, 1.0] {
        assert!(Jitter::try_from(sample).is_err());
    }
    let mut driver = SyncDriver::new(String::new());
    driver.step(
        u64::MAX - 1,
        SyncInput::Connect {
            config: Config { hub: 1, credential: 1 },
        },
    );
    assert_eq!(driver.state().timers[0].at, u64::MAX);
}

#[test]
fn refusal_during_a_drop_and_late_grace_after_recovery() {
    let mut driver = SyncDriver::new(String::new());
    let event = |value| serde_json::from_value(value).unwrap();
    driver.step(0, event(json!({"kind":"connect", "config":{"hub":1,"credential":1}})));
    let frame = |generation| event(json!({"kind":"frame", "generation":generation, "meta":{"kind":"broadcast"}}));
    driver.step(1, frame(1));
    let fail = |generation, http_status| {
        event(json!({"kind":"socket_failed", "generation":generation, "http_status":http_status, "jitter":0}))
    };
    driver.step(100, fail(1, None));
    let first_grace = driver.state().timers[0];
    let retry = driver.state().timers[1];
    driver.step(retry.at, SyncInput::Tick { timer: retry });
    driver.step(500, fail(2, Some(401)));
    assert_eq!(driver.state().timers[1].at, 60_500);
    driver.step(600, SyncInput::ReconnectNow);
    let recovered = driver.step(700, frame(3));
    assert!(recovered.contains(&SyncEffect::CancelTimer { timer: first_grace }));
    assert!(driver.state().timers.is_empty() && !driver.state().rejected);
    driver.step(800, fail(3, None));
    let second_drop = driver.state().clone();
    assert!(
        driver
            .step(first_grace.at, SyncInput::Tick { timer: first_grace })
            .is_empty()
    );
    assert_eq!(driver.state(), &second_drop);
}
