//! The core's entry points (docs/plans/plan-rust-core.md, "The boundary: a few entry points and bytes"): C functions
//! that Swift calls directly (`include/persona_core.h`) and JNI functions that Kotlin declares as `external fun`
//! (`jni`). Both carry bincode-encoded wire types. A panic never crosses the boundary: each entry point catches it and
//! returns its failure value.

mod jni;

use std::ffi::c_void;
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Arc;
use std::time::Duration;

use persona_core_runtime::wire::{self, Config};
use persona_core_runtime::{Host, Runtime};

/// The C client's callbacks (`PersonaCoreHost`).
#[repr(C)]
pub struct PersonaCoreHost {
    pub context: *mut c_void,
    pub changed: Option<unsafe extern "C" fn(*mut c_void, u64)>,
    pub run: Option<unsafe extern "C" fn(*mut c_void, *const u8, usize)>,
    pub release: Option<unsafe extern "C" fn(*mut c_void)>,
}

/// The header requires `changed` and `run` to be callable from any thread with this context.
struct CHost(PersonaCoreHost);
unsafe impl Send for CHost {}
unsafe impl Sync for CHost {}

impl Host for CHost {
    fn changed(&self, version: u64) {
        if let Some(changed) = self.0.changed {
            unsafe { changed(self.0.context, version) }
        }
    }
    fn run(&self, effect: &[u8]) {
        if let Some(run) = self.0.run {
            unsafe { run(self.0.context, effect.as_ptr(), effect.len()) }
        }
    }
}

impl Drop for CHost {
    fn drop(&mut self) {
        if let Some(release) = self.0.release {
            unsafe { release(self.0.context) }
        }
    }
}

/// An opaque handle to a running core.
pub struct PersonaCore {
    runtime: Runtime,
}

#[repr(C)]
pub struct PersonaCoreBytes {
    pub data: *mut u8,
    pub length: usize,
    pub capacity: usize,
}

impl PersonaCoreBytes {
    fn empty() -> Self {
        PersonaCoreBytes {
            data: std::ptr::null_mut(),
            length: 0,
            capacity: 0,
        }
    }
    fn from_vec(bytes: Vec<u8>) -> Self {
        let mut bytes = std::mem::ManuallyDrop::new(bytes);
        PersonaCoreBytes {
            data: bytes.as_mut_ptr(),
            length: bytes.len(),
            capacity: bytes.capacity(),
        }
    }
}

/// # Safety
/// `pointer` is null with `length` 0, or valid for `length` bytes.
unsafe fn slice<'a>(pointer: *const u8, length: usize) -> &'a [u8] {
    if pointer.is_null() || length == 0 {
        &[]
    } else {
        unsafe { std::slice::from_raw_parts(pointer, length) }
    }
}

pub(crate) fn start(config: &[u8], host: Arc<dyn Host>) -> Option<Box<PersonaCore>> {
    let config: Config = wire::decode(config).ok()?;
    Some(Box::new(PersonaCore {
        runtime: Runtime::new(config, host),
    }))
}

/// # Safety
/// `config` is valid for `length` bytes; the host's callbacks follow `persona_core.h`.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_new(host: PersonaCoreHost, config: *const u8, length: usize) -> *mut PersonaCore {
    let host = CHost(host);
    let config = unsafe { slice(config, length) };
    catch_unwind(AssertUnwindSafe(|| start(config, Arc::new(host))))
        .ok()
        .flatten()
        .map_or(std::ptr::null_mut(), Box::into_raw)
}

/// # Safety
/// `core` came from `persona_core_new` and was not freed; `action` is valid for `length` bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_dispatch(core: *mut PersonaCore, action: *const u8, length: usize) -> bool {
    let Some(core) = (unsafe { core.as_ref() }) else {
        return false;
    };
    let action = unsafe { slice(action, length) };
    catch_unwind(AssertUnwindSafe(|| core.runtime.dispatch(action))).unwrap_or(false)
}

/// # Safety
/// `core` came from `persona_core_new` and was not freed; `chat` is valid UTF-8 for `length` bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_drain(
    core: *mut PersonaCore,
    chat: *const u8,
    length: usize,
) -> PersonaCoreBytes {
    let Some(core) = (unsafe { core.as_ref() }) else {
        return PersonaCoreBytes::empty();
    };
    let Ok(chat) = std::str::from_utf8(unsafe { slice(chat, length) }) else {
        return PersonaCoreBytes::empty();
    };
    catch_unwind(AssertUnwindSafe(|| core.runtime.drain(chat)))
        .map_or_else(|_| PersonaCoreBytes::empty(), PersonaCoreBytes::from_vec)
}

/// # Safety
/// `bytes` came from `persona_core_drain` and is freed once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_bytes_free(bytes: PersonaCoreBytes) {
    if !bytes.data.is_null() {
        drop(unsafe { Vec::from_raw_parts(bytes.data, bytes.length, bytes.capacity) });
    }
}

/// # Safety
/// `core` came from `persona_core_new` and was not freed.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_flush(core: *mut PersonaCore, timeout_ms: u32) -> bool {
    let Some(core) = (unsafe { core.as_ref() }) else {
        return false;
    };
    catch_unwind(AssertUnwindSafe(|| {
        core.runtime.flush(Duration::from_millis(timeout_ms.into()))
    }))
    .unwrap_or(false)
}

/// # Safety
/// `core` came from `persona_core_new` and is freed once.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_core_free(core: *mut PersonaCore) {
    if !core.is_null() {
        let core = unsafe { Box::from_raw(core) };
        let _ = catch_unwind(AssertUnwindSafe(move || drop(core)));
    }
}
