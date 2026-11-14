//! JNI entry points for `persona.core.rustcore.NativeBridge` (android/core/rustcore). Kotlin calls them directly, with
//! no JNA and no C layer. The host's `changed` and `run` are looked up once and called on the core's worker thread,
//! which stays attached to the JVM.

use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Arc;
#[cfg(target_os = "android")]
use std::time::Duration;

use jni::JNIEnv;
use jni::JavaVM;
use jni::objects::{GlobalRef, JByteArray, JClass, JMethodID, JObject, JString};
use jni::signature::{Primitive, ReturnType};
#[cfg(target_os = "android")]
use jni::sys::jlongArray;
use jni::sys::{JNI_FALSE, JNI_TRUE, jboolean, jbyteArray, jint, jlong, jvalue};
use persona_core_runtime::Host;

use crate::{PersonaCore, create};

struct JHost {
    vm: JavaVM,
    host: GlobalRef,
    changed: JMethodID,
    run: JMethodID,
}

impl JHost {
    /// Calls a `void` method of the host with one argument, on the core's worker thread.
    fn call(&self, env: &mut JNIEnv, method: JMethodID, argument: jvalue) {
        // The method ids were looked up on this object's class, with these signatures.
        let _ = unsafe {
            env.call_method_unchecked(
                self.host.as_obj(),
                method,
                ReturnType::Primitive(Primitive::Void),
                &[argument],
            )
        };
        if env.exception_check().unwrap_or(false) {
            let _ = env.exception_clear();
        }
    }
}

impl Host for JHost {
    fn changed(&self, version: u64) {
        let Ok(mut env) = self.vm.attach_current_thread_permanently() else {
            return;
        };
        self.call(&mut env, self.changed, jvalue { j: version as jlong });
    }
    fn run(&self, effect: &[u8]) {
        let Ok(mut env) = self.vm.attach_current_thread_permanently() else {
            return;
        };
        let Ok(array) = env.byte_array_from_slice(effect) else {
            return;
        };
        self.call(&mut env, self.run, jvalue { l: array.as_raw() });
        // The worker never returns to Java, so its local references are freed by hand.
        let _ = env.delete_local_ref(array);
    }
}

fn core(handle: jlong) -> Option<Arc<persona_core_runtime::Runtime>> {
    if handle == 0 {
        return None;
    }
    crate::runtime(handle as usize as *const PersonaCore)
}

fn boolean(value: bool) -> jboolean {
    if value { JNI_TRUE } else { JNI_FALSE }
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_create(
    mut env: JNIEnv,
    _class: JClass,
    host: JObject,
    config: JByteArray,
) -> jlong {
    catch_unwind(AssertUnwindSafe(|| {
        let config = env.convert_byte_array(&config).ok()?;
        let class = env.get_object_class(&host).ok()?;
        let changed = env.get_method_id(&class, "changed", "(J)V").ok()?;
        let run = env.get_method_id(&class, "run", "([B)V").ok()?;
        let host = JHost {
            vm: env.get_java_vm().ok()?,
            host: env.new_global_ref(&host).ok()?,
            changed,
            run,
        };
        create(&config, Arc::new(host))
    }))
    .ok()
    .flatten()
    .map_or(0, |core| core as jlong)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_dispatch(
    env: JNIEnv,
    _class: JClass,
    handle: jlong,
    action: JByteArray,
) -> jboolean {
    let Some(runtime) = core(handle) else { return JNI_FALSE };
    catch_unwind(AssertUnwindSafe(|| {
        let Ok(action) = env.convert_byte_array(&action) else {
            return false;
        };
        runtime.dispatch(&action)
    }))
    .map_or(JNI_FALSE, boolean)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_drain(
    mut env: JNIEnv,
    _class: JClass,
    handle: jlong,
    chat: JString,
) -> jbyteArray {
    let Some(runtime) = core(handle) else {
        return std::ptr::null_mut();
    };
    catch_unwind(AssertUnwindSafe(|| {
        let chat: String = env.get_string(&chat).ok()?.into();
        let patch = runtime.drain(&chat);
        env.byte_array_from_slice(&patch).ok().map(JByteArray::into_raw)
    }))
    .ok()
    .flatten()
    .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_flush(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
    timeout_ms: jint,
) -> jboolean {
    if handle != 0 {
        boolean(unsafe { crate::persona_core_flush(handle as *mut PersonaCore, timeout_ms.max(0) as u32) })
    } else {
        JNI_FALSE
    }
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_destroy(_env: JNIEnv, _class: JClass, handle: jlong) {
    if handle != 0 {
        unsafe { crate::persona_core_free(handle as *mut PersonaCore) }
    }
}

#[cfg(target_os = "android")]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_enableDiagnostics(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
) {
    let _ = catch_unwind(AssertUnwindSafe(|| {
        if let Some(runtime) = core(handle) {
            runtime.enable_diagnostics();
        }
    }));
}

#[cfg(target_os = "android")]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_flushDiagnostic(
    _env: JNIEnv,
    _class: JClass,
    handle: jlong,
    timeout_ms: jint,
) -> jboolean {
    let Some(runtime) = core(handle) else { return JNI_FALSE };
    let timeout = Duration::from_millis(timeout_ms.max(0) as u64);
    catch_unwind(AssertUnwindSafe(|| runtime.flush_diagnostic(timeout))).map_or(JNI_FALSE, boolean)
}

#[cfg(target_os = "android")]
#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeDiagnostics_diagnostics(
    env: JNIEnv,
    _class: JClass,
    handle: jlong,
) -> jlongArray {
    let Some(runtime) = core(handle) else {
        return std::ptr::null_mut();
    };
    let counters = catch_unwind(AssertUnwindSafe(|| runtime.diagnostics())).unwrap_or_default();
    env.new_long_array(counters.len() as i32)
        .and_then(|array| {
            env.set_long_array_region(&array, 0, &counters)?;
            Ok(array.into_raw())
        })
        .unwrap_or(std::ptr::null_mut())
}
