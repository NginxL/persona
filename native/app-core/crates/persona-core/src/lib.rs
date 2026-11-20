//! Persona's client logic without IO, clocks or FFI (docs/plans/plan-rust-core.md). Everything comes in as an event and
//! goes out as state and requests, so the shared fixtures (`spec/fixtures/`) replay against it directly.

#![forbid(unsafe_code)]

mod apple_text;
mod compat;

pub mod answer;
pub mod continuity;
pub mod item;
pub mod recents;
pub mod sort;
pub mod state;
pub mod stream_chunks;
pub mod stream_repair;
pub mod sync;
pub mod task_index;
pub mod transport;
pub mod turns;
pub mod viewed_images;
pub mod wire_envelope;

pub use item::Item;
