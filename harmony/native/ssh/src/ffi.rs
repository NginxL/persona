use crate::tunnel::{self, Config};
use std::{ffi::{CStr, CString, c_char}, panic::{AssertUnwindSafe, catch_unwind}};
use std::net::IpAddr;
use zeroize::Zeroizing;

#[repr(C)]
pub struct SshConfig {
    pub id: *const c_char, pub ssh_host: *const c_char, pub ssh_port: u16,
    pub user: *const c_char, pub host_key: *const c_char, pub target_host: *const c_char, pub target_port: u16,
    pub resolved_hub_ips: *const *const c_char, pub resolved_hub_ips_len: usize,
    pub private_key: *const u8, pub private_key_len: usize, pub passphrase: *const u8, pub passphrase_len: usize,
}

unsafe fn text(pointer: *const c_char, max: usize) -> Result<String, &'static str> {
    if pointer.is_null() { return Err("ssh_invalid_config"); }
    let value = unsafe { CStr::from_ptr(pointer) }.to_str().map_err(|_| "ssh_invalid_config")?;
    if value.len() > max { return Err("ssh_invalid_config"); } Ok(value.into())
}
unsafe fn secret(pointer: *const u8, length: usize, max: usize) -> Result<Zeroizing<Vec<u8>>, &'static str> {
    if length > max || pointer.is_null() && length != 0 { return Err("ssh_invalid_key"); }
    Ok(Zeroizing::new(if length == 0 { Vec::new() } else { unsafe { std::slice::from_raw_parts(pointer, length) }.to_vec() }))
}
unsafe fn resolved_hub_ips(pointer: *const *const c_char, length: usize) -> Result<Vec<IpAddr>, &'static str> {
    if length > 16 || pointer.is_null() && length != 0 { return Err("ssh_invalid_config"); }
    let values = if length == 0 { &[] } else { unsafe { std::slice::from_raw_parts(pointer, length) } };
    let mut result = Vec::with_capacity(length);
    for value in values {
        let address = unsafe { text(*value, 45) }?.parse::<IpAddr>().map_err(|_| "ssh_invalid_config")?;
        if !result.contains(&address) { result.push(address); }
    }
    Ok(result)
}
fn json_result(value: serde_json::Value) -> *mut c_char {
    CString::new(value.to_string()).expect("json_has_no_nul").into_raw()
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_begin(id: *const c_char) -> bool {
    catch_unwind(AssertUnwindSafe(|| unsafe { text(id, 36) }.and_then(|id| tunnel::begin(&id)).is_ok())).unwrap_or(false)
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_open(raw: *const SshConfig) -> *mut c_char {
    let result = catch_unwind(AssertUnwindSafe(|| -> Result<serde_json::Value, &'static str> {
        if raw.is_null() { return Err("ssh_invalid_config"); }
        let raw = unsafe { &*raw }; let id = unsafe { text(raw.id, 36) }?;
        let config = Config { ssh_host: unsafe { text(raw.ssh_host, 253) }?, ssh_port: raw.ssh_port,
            user: unsafe { text(raw.user, 128) }?, host_key: unsafe { text(raw.host_key, 50) }?,
            target_host: unsafe { text(raw.target_host, 253) }?, target_port: raw.target_port,
            resolved_hub_ips: unsafe { resolved_hub_ips(raw.resolved_hub_ips, raw.resolved_hub_ips_len) }?,
            private_key: unsafe { secret(raw.private_key, raw.private_key_len, 32768) }?,
            passphrase: unsafe { secret(raw.passphrase, raw.passphrase_len, 4096) }? };
        let lease = tunnel::open(&id, config)?;
        Ok(serde_json::json!({ "id": id, "host": "127.0.0.1", "port": lease.port, "username": lease.username, "password": lease.password }))
    }));
    match result {
        Ok(Ok(value)) => json_result(value),
        Ok(Err(error)) => json_result(serde_json::json!({ "error": error })),
        Err(_) => json_result(serde_json::json!({ "error": "ssh_native_failed" })),
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_cancel(id: *const c_char) {
    let _ = catch_unwind(AssertUnwindSafe(|| { if let Ok(id) = unsafe { text(id, 36) } { tunnel::cancel(&id); } }));
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_abandon(id: *const c_char) {
    let _ = catch_unwind(AssertUnwindSafe(|| { if let Ok(id) = unsafe { text(id, 36) } { tunnel::abandon(&id); } }));
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_close(id: *const c_char) {
    let _ = catch_unwind(AssertUnwindSafe(|| { if let Ok(id) = unsafe { text(id, 36) } { tunnel::close(&id); } }));
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_alive(id: *const c_char) -> bool {
    catch_unwind(AssertUnwindSafe(|| unsafe { text(id, 36) }.is_ok_and(|id| tunnel::alive(&id)))).unwrap_or(false)
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn persona_ssh_free(value: *mut c_char) {
    if !value.is_null() {
        let mut bytes = unsafe { CString::from_raw(value) }.into_bytes_with_nul();
        use zeroize::Zeroize; bytes.zeroize();
    }
}
