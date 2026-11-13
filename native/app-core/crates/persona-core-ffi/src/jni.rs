//! JNI entry points for `persona.core.rustcore.NativeBridge` (android/core/rustcore). Kotlin calls them directly, with
//! no JNA and no C layer. The host's `changed` and `run` are looked up once and called on the core's worker thread,
//! which stays attached to the JVM.

use std::panic::{AssertUnwindSafe, catch_unwind};
use std::sync::Arc;
use std::time::Duration;

use jni::JNIEnv;
use jni::JavaVM;
use jni::objects::{GlobalRef, JByteArray, JClass, JMethodID, JObject, JString};
use jni::signature::{Primitive, ReturnType};
use jni::sys::{JNI_FALSE, JNI_TRUE, jboolean, jbyteArray, jint, jlong, jvalue};
use persona_core_runtime::Host;

use crate::{PersonaCore, start};

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

fn core<'a>(handle: jlong) -> Option<&'a PersonaCore> {
    unsafe { (handle as *const PersonaCore).as_ref() }
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
        start(&config, Arc::new(host))
    }))
    .ok()
    .flatten()
    .map_or(0, |core| Box::into_raw(core) as jlong)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_dispatch(
    env: JNIEnv,
    _class: JClass,
    handle: jlong,
    action: JByteArray,
) -> jboolean {
    let Some(core) = core(handle) else { return JNI_FALSE };
    catch_unwind(AssertUnwindSafe(|| {
        let Ok(action) = env.convert_byte_array(&action) else {
            return false;
        };
        core.runtime.dispatch(&action)
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
    let Some(core) = core(handle) else {
        return std::ptr::null_mut();
    };
    catch_unwind(AssertUnwindSafe(|| {
        let chat: String = env.get_string(&chat).ok()?.into();
        let patch = core.runtime.drain(&chat);
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
    let Some(core) = core(handle) else { return JNI_FALSE };
    let timeout = Duration::from_millis(timeout_ms.max(0) as u64);
    catch_unwind(AssertUnwindSafe(|| core.runtime.flush(timeout))).map_or(JNI_FALSE, boolean)
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_persona_core_rustcore_NativeBridge_destroy(_env: JNIEnv, _class: JClass, handle: jlong) {
    if handle != 0 {
        let core = unsafe { Box::from_raw(handle as *mut PersonaCore) };
        let _ = catch_unwind(AssertUnwindSafe(move || drop(core)));
    }
}
