# Persona native SSH transport

This bridge uses public `russh` and Tokio APIs. A user's SSH private key and optional passphrase authenticate only after the exact `SHA256:` server host fingerprint matches. Server certificates, implicit host trust, password authentication and RSA-SHA1 fallback are rejected.

OpenSSH keys support optional passphrases with bcrypt work factors up to 64 rounds. Unencrypted PEM and legacy PEM AES keys are supported. Encrypted PKCS#8 and PuTTY imports require conversion to OpenSSH because their independently chosen work factors would defeat bounded cancellation. RSA keys are limited to 8192 bits and require RSA-SHA2 authentication.

The native library receives SSH settings, the captured Hub hostname/port, bounded resolved IP hints and temporary key bytes. It does not receive a Hub bearer. The ArkTS transport retains the logical HTTPS URL, Host/SNI, path prefix and SPKI pin, and sends application authorization only after TLS admission.

## Proxy and ownership contract

The loopback listener supports authenticated HTTP CONNECT and SOCKS5 CONNECT to the captured Hub. CONNECT requires the exact logical authority. API 26 TLSSocket can supply a resolved IP in its SOCKS request, so the model captures at most 16 fresh device DNS answers before reading credentials or opening SSH. SOCKS admits only the captured name or those IPs at the fixed port. Its actual SSH direct-tcpip destination always remains the original Hub hostname/port. The phone must be able to resolve that hostname; failed, empty, timed-out or stale DNS fails closed.

The listener binds `127.0.0.1`, uses a random per-tunnel credential and permits at most four concurrent channels. Handshake data is limited to 8 KiB, handshakes to 10 seconds, startup to 20 seconds and idle connections to 60 seconds. Cancellation reaches DNS, queued starts, initial key exchange, established SSH, the listener and channel tasks. NAPI signals cancellation synchronously before worker cleanup; close is idempotent.

`SshTunnel` captures the client, profile, manager scope, connection generation, pairing revision, endpoint, pin, storage epoch and foreground admission. A stale result cannot install a lease. Enabling, disabling or replacing SSH configuration retires that Hub's current and retained manager connections before they rebind. Re-pairing and Forget revoke pending opens and existing leases. Enabled SSH failures never probe the Hub directly.

Private keys use 1 KiB Asset Store chunks plus a final length manifest. Limits are 32 KiB for a key and 4 KiB for its passphrase. Assets use device-unlocked access, no user-auth challenge and synchronization disabled. Database metadata contains only a random key reference. Per-profile serialized writes and retirement epochs prevent delayed work from restoring forgotten secrets.

## Build

Use Rust 1.98.0 with `aarch64-unknown-linux-ohos`, the HarmonyOS API 26 native SDK and its CMake/Ninja tools. Resolve the committed Cargo lock before an offline build:

```sh
rustup run 1.98.0 cargo fetch --locked
rustup run 1.98.0 cargo test --locked --offline
OHOS_SDK_NATIVE=/path/to/api26/native ./build-ohos.sh
```

`OHOS_SDK_NATIVE` names the directory containing `llvm`, `sysroot` and `build/cmake`. The build script resolves the pinned compiler explicitly when rustup's compiler shims are absent. It enables AWS-LC's bindgen library feature for OHOS and uses the SDK's libclang/sysroot. No bindgen CLI is required.

Then build the HAP from `harmony/`. The entry CMake project imports `target/aarch64-unknown-linux-ohos/release/libpersona_ssh_core.so`, builds `libpersona_ssh.so`, and packages both ARM64 shared libraries. `PERSONA_SSH_LIBRARY` can override the core path for a controlled build. Generated binaries stay outside version control.

## Dependency notices

The committed Cargo lock and `licenses/locked-inventory.json` cover the complete dependency closure. Regenerate notices only when that lock changes:

```sh
python3 collect-licenses.py --registry-src "$HOME/.cargo/registry/src"
```

The collector requires Python 3.11+ and does not fetch files. Some published crate archives omit original upstream notices; `licenses/upstream-notices.json` pins retained files to each published VCS commit and records source URLs and SHA256 values. Collection checks those identities and hashes before accepting them.

Bundle the reviewed notice text into the app's local rawfile after collecting or changing the inventory:

```sh
python3 package-notices.py
```

This standard-library-only generator works with Python 3.9+ and runs offline. It checks every notice SHA256 and validates that the Cargo package/version set and registry checksums exactly match `Cargo.lock`. Cargo notices must be listed in `licenses/locked-inventory.json` and confined to their crate directories.

The HAP also ships the libc++ shared runtime from the HarmonyOS Native SDK. Its full dual-license notice is retained in `licenses/sdk-runtime/libcxx-license.txt`, separately from Cargo crates. `licenses/sdk-runtime/supplemental-inventory.json` pins its SDK version, relative SDK source paths, source-content hashes, provenance, and bundled notice hash. The source text was copied from the SDK's `native/NOTICE.txt` and cross-checked against `native/llvm/NOTICE`; the public inventory contains no host-specific SDK path. The generator checks the bundled supplemental notice and emits only supplemental files listed in that inventory.

The output is `harmony/entry/src/main/resources/rawfile/native-ssh-licenses.html`, which the HAP packages as an ordinary rawfile resource. Regenerate it after changing either inventory or any listed notice, and review/commit the inventories, source notices and generated HTML together. Update the supplemental metadata when the app's bundled Native SDK runtime changes.

## Verification

`harmony/tests/ssh-tunnel.test.mjs` loads the actual model and vault with native-kit boundaries replaced. Rust fixtures exercise strict CONNECT/SOCKS admission, captured-IP routing to the original logical Hub, oversized/incomplete handshakes, wrong host keys with an authentication counter, and cancellation during initial key exchange.

The optional native `sshFixtureFile` test suite exercises actual Asset Store chunks, both proxy APIs, TLS pinning, WebSocket/child streams and lease retirement. Supply an owned test-only configuration in the test Ability's private files directory; never commit its key. Run hostname and IP targets separately. Portable fixtures and compilation do not substitute for these native checks or for foreground/background and profile lifecycle verification.
