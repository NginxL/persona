use crate::proxy::{self, Credentials, Result};
use base64::{Engine, engine::general_purpose::{STANDARD_NO_PAD, URL_SAFE_NO_PAD}};
use russh::{client, keys::{HashAlg, PrivateKeyWithHashAlg, PublicKeyOrCertificate}};
use std::{collections::HashMap, future::Future, io, pin::Pin, sync::{Arc, Condvar, Mutex, OnceLock, atomic::{AtomicBool, Ordering}}, task::{Context, Poll}, time::{Duration, Instant}};
use std::net::IpAddr;
use tokio::{io::{AsyncRead, AsyncWrite, ReadBuf}, net::{TcpListener, TcpStream}, runtime::Runtime, task::JoinSet};
use tokio_util::sync::CancellationToken;
use zeroize::Zeroizing;

const MAX_TUNNELS: usize = 8;
const MAX_CHANNELS: usize = 4;
const CONNECT_TIMEOUT: Duration = Duration::from_secs(20);
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);
const IDLE_TIMEOUT: Duration = Duration::from_secs(60);

pub(crate) struct Config {
    pub ssh_host: String, pub ssh_port: u16, pub user: String, pub host_key: String,
    pub target_host: String, pub target_port: u16, pub resolved_hub_ips: Vec<IpAddr>,
    pub private_key: Zeroizing<Vec<u8>>, pub passphrase: Zeroizing<Vec<u8>>,
}

pub(crate) struct Lease { pub port: u16, pub username: String, pub password: String }

struct Control {
    cancelled: CancellationToken,
    finished: (Mutex<bool>, Condvar),
}
impl Control {
    fn new() -> Self { Self { cancelled: CancellationToken::new(), finished: (Mutex::new(false), Condvar::new()) } }
    fn finish(&self) {
        *self.finished.0.lock().unwrap_or_else(|e| e.into_inner()) = true;
        self.finished.1.notify_all();
    }
}

fn registry() -> &'static Mutex<HashMap<String, Arc<Control>>> {
    static REGISTRY: OnceLock<Mutex<HashMap<String, Arc<Control>>>> = OnceLock::new();
    REGISTRY.get_or_init(|| Mutex::new(HashMap::new()))
}
fn runtime() -> &'static Runtime {
    static RUNTIME: OnceLock<Runtime> = OnceLock::new();
    RUNTIME.get_or_init(|| tokio::runtime::Builder::new_multi_thread().worker_threads(2).enable_all().build().expect("ssh_runtime"))
}
pub(crate) fn valid_id(id: &str) -> bool { id.len() == 36 && id.bytes().all(|c| c.is_ascii_hexdigit() || c == b'-') }
pub(crate) fn valid_pin(pin: &str) -> bool {
    pin.strip_prefix("SHA256:").and_then(|p| STANDARD_NO_PAD.decode(p).ok()).is_some_and(|bytes| bytes.len() == 32)
        && pin.len() == 50
}
pub(crate) fn begin(id: &str) -> Result<()> {
    if !valid_id(id) { return Err("ssh_invalid_config"); }
    let mut states = registry().lock().unwrap_or_else(|e| e.into_inner());
    if states.contains_key(id) || states.len() >= MAX_TUNNELS { return Err("ssh_busy"); }
    states.insert(id.into(), Arc::new(Control::new())); Ok(())
}
fn retire(id: &str, control: &Arc<Control>) {
    let mut states = registry().lock().unwrap_or_else(|e| e.into_inner());
    if states.get(id).is_some_and(|entry| Arc::ptr_eq(entry, control)) { states.remove(id); }
    control.finish();
}
pub(crate) fn close(id: &str) {
    let control = registry().lock().unwrap_or_else(|e| e.into_inner()).remove(id);
    if let Some(control) = control {
        control.cancelled.cancel();
        // A not-yet-scheduled open owns no resources and will observe cancellation/missing registration.
        let done = control.finished.0.lock().unwrap_or_else(|e| e.into_inner());
        let _ = control.finished.1.wait_timeout_while(done, Duration::from_secs(2), |finished| !*finished);
    }
}
pub(crate) fn cancel(id: &str) {
    if let Some(control) = registry().lock().unwrap_or_else(|e| e.into_inner()).get(id) { control.cancelled.cancel(); }
}
// Error cleanup on the JS thread must signal teardown without waiting for worker completion.
pub(crate) fn abandon(id: &str) {
    if let Some(control) = registry().lock().unwrap_or_else(|e| e.into_inner()).remove(id) { control.cancelled.cancel(); }
}
pub(crate) fn alive(id: &str) -> bool {
    registry().lock().unwrap_or_else(|e| e.into_inner()).get(id).is_some_and(|control| !control.cancelled.is_cancelled())
}
pub(crate) fn open(id: &str, config: Config) -> Result<Lease> {
    let control = registry().lock().unwrap_or_else(|e| e.into_inner()).get(id).cloned().ok_or("ssh_cancelled")?;
    let result = runtime().block_on(async {
        tokio::select! {
            biased;
            _ = control.cancelled.cancelled() => Err("ssh_cancelled"),
            result = tokio::time::timeout(CONNECT_TIMEOUT, connect(id, config, control.clone())) => result.unwrap_or(Err("ssh_timeout")),
        }
    });
    // Cancellation can wake the inner socket after select's first branch was polled.
    let result = if control.cancelled.is_cancelled() { Err("ssh_cancelled") } else { result };
    if result.is_err() { control.cancelled.cancel(); retire(id, &control); }
    result
}

struct HostVerifier { pin: String, rejected: Arc<AtomicBool> }
impl client::Handler for HostVerifier {
    type Error = russh::Error;
    async fn check_server_key(&mut self, key: &PublicKeyOrCertificate) -> std::result::Result<bool, Self::Error> {
        let valid = matches!(key, PublicKeyOrCertificate::PublicKey { key, .. } if key.fingerprint(HashAlg::Sha256).to_string() == self.pin);
        if !valid { self.rejected.store(true, Ordering::SeqCst); }
        Ok(valid)
    }
}

fn validate(config: &Config) -> Result<()> {
    if !proxy::valid_host(&config.ssh_host) || !proxy::valid_host(&config.target_host) || config.ssh_port == 0 || config.target_port == 0 ||
        config.user.is_empty() || config.user.len() > 128 || config.user.chars().any(char::is_control) || !valid_pin(&config.host_key) ||
        config.resolved_hub_ips.len() > 16 ||
        (config.target_host.parse::<IpAddr>().is_ok() && !config.resolved_hub_ips.is_empty()) ||
        (config.target_host.parse::<IpAddr>().is_err() && config.resolved_hub_ips.is_empty()) ||
        config.private_key.is_empty() || config.private_key.len() > 32768 || config.passphrase.len() > 4096 {
        return Err("ssh_invalid_config");
    }
    Ok(())
}

fn decode_key(text: &str, passphrase: &str) -> Result<russh::keys::PrivateKey> {
    use russh::keys::{PrivateKey, ssh_key::Kdf};
    let key = if text.trim().starts_with("-----BEGIN OPENSSH PRIVATE KEY-----") {
        let encoded = PrivateKey::from_openssh(text).map_err(|_| "ssh_invalid_key")?;
        if let Kdf::Bcrypt { salt, rounds } = encoded.kdf() {
            // A user-provided encrypted file must not stall cancellation with an arbitrary KDF work factor.
            if *rounds == 0 || *rounds > 64 || salt.len() > 64 { return Err("ssh_invalid_key"); }
        }
        if encoded.is_encrypted() { encoded.decrypt(passphrase).map_err(|_| "ssh_invalid_key")? } else { encoded }
    } else {
        // These formats carry independently chosen, potentially unbounded PBKDF/Argon2 work factors.
        // Import them as OpenSSH keys instead. Unencrypted PEM and legacy PEM AES remain supported.
        if text.trim().starts_with("PuTTY-User-Key-File-") || text.contains("-----BEGIN ENCRYPTED PRIVATE KEY-----") {
            return Err("ssh_invalid_key");
        }
        russh::keys::decode_secret_key(text, if passphrase.is_empty() { None } else { Some(passphrase) }).map_err(|_| "ssh_invalid_key")?
    };
    if key.public_key().key_data().rsa().is_some_and(|rsa| rsa.key_size() > 8192) { return Err("ssh_invalid_key"); }
    Ok(key)
}

async fn connect(id: &str, config: Config, control: Arc<Control>) -> Result<Lease> {
    validate(&config)?;
    let rejected = Arc::new(AtomicBool::new(false));
    let client_config = client::Config {
        window_size: 256 * 1024, maximum_packet_size: 32768, channel_buffer_size: 4,
        inactivity_timeout: Some(IDLE_TIMEOUT), keepalive_interval: Some(Duration::from_secs(20)), keepalive_max: 2,
        nodelay: true, ..Default::default()
    };
    let tcp = TcpStream::connect((config.ssh_host.as_str(), config.ssh_port)).await.map_err(|_| "ssh_connect_failed")?;
    let socket = CancelableStream { inner: tcp, cancelled: Box::pin(control.cancelled.clone().cancelled_owned()) };
    let mut ssh = client::connect_stream(Arc::new(client_config), socket,
        HostVerifier { pin: config.host_key.clone(), rejected: rejected.clone() }).await
        .map_err(|_| if rejected.load(Ordering::SeqCst) { "ssh_host_key_mismatch" } else { "ssh_connect_failed" })?;
    if control.cancelled.is_cancelled() { return Err("ssh_cancelled"); }
    // Authentication is unreachable until the same connection's pinned host-key check has succeeded.
    let key_text = std::str::from_utf8(&config.private_key).map_err(|_| "ssh_invalid_key")?;
    let passphrase = std::str::from_utf8(&config.passphrase).map_err(|_| "ssh_invalid_key")?;
    let key = decode_key(key_text, passphrase)?;
    let hash = if key.algorithm().is_rsa() {
        let hash = ssh.best_supported_rsa_hash().await.map_err(|_| "ssh_auth_failed")?.flatten().ok_or("ssh_auth_failed")?;
        if !matches!(hash, HashAlg::Sha256 | HashAlg::Sha512) { return Err("ssh_auth_failed"); }
        Some(hash)
    } else { None };
    if control.cancelled.is_cancelled() { return Err("ssh_cancelled"); }
    let result = ssh.authenticate_publickey(&config.user, PrivateKeyWithHashAlg::new(Arc::new(key), hash)).await.map_err(|_| "ssh_auth_failed")?;
    if !result.success() { return Err("ssh_auth_failed"); }
    if control.cancelled.is_cancelled() { return Err("ssh_cancelled"); }
    let listener = TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0)).await.map_err(|_| "ssh_proxy_failed")?;
    let port = listener.local_addr().map_err(|_| "ssh_proxy_failed")?.port();
    let mut random = [0u8; 32]; getrandom::fill(&mut random).map_err(|_| "ssh_proxy_failed")?;
    let credentials = Credentials { username: "persona".into(), password: URL_SAFE_NO_PAD.encode(random) };
    let lease = Lease { port, username: credentials.username.clone(), password: credentials.password.clone() };
    let host = config.target_host.clone(); let target_port = config.target_port; let resolved_hub_ips = config.resolved_hub_ips.clone();
    drop(config); // Key text and passphrase buffers are wiped before returning the local endpoint.
    let id = id.to_owned();
    runtime().spawn(async move {
        serve(listener, Arc::new(ssh), host, target_port, resolved_hub_ips, credentials, control.clone()).await;
        control.cancelled.cancel(); retire(&id, &control);
    });
    Ok(lease)
}

async fn serve(listener: TcpListener, ssh: Arc<client::Handle<HostVerifier>>, host: String, port: u16,
    resolved_hub_ips: Vec<IpAddr>, credentials: Credentials, control: Arc<Control>) {
    let activity = Arc::new(Mutex::new(Instant::now()));
    let mut tasks = JoinSet::new(); let mut tick = tokio::time::interval(Duration::from_secs(1));
    loop {
        tokio::select! {
            biased;
            _ = control.cancelled.cancelled() => break,
            _ = tick.tick() => {
                if ssh.is_closed() || activity.lock().unwrap_or_else(|e| e.into_inner()).elapsed() >= IDLE_TIMEOUT { break; }
            },
            _ = tasks.join_next(), if !tasks.is_empty() => {},
            accepted = listener.accept() => {
                let Ok((stream, peer)) = accepted else { break; };
                if !peer.ip().is_loopback() || tasks.len() >= MAX_CHANNELS { drop(stream); continue; }
                let ssh = ssh.clone(); let host = host.clone(); let resolved_hub_ips = resolved_hub_ips.clone(); let credentials = credentials.clone();
                let cancelled = control.cancelled.clone(); let activity = activity.clone();
                tasks.spawn(async move {
                    tokio::select! {
                        biased;
                        _ = cancelled.cancelled() => {},
                        _ = forward(stream, ssh, &host, port, &resolved_hub_ips, &credentials, activity) => {},
                    }
                });
            }
        }
    }
    drop(listener); tasks.abort_all(); while tasks.join_next().await.is_some() {}
    let _ = tokio::time::timeout(Duration::from_secs(1), ssh.disconnect(russh::Disconnect::ByApplication, "", "")).await;
}

async fn forward(mut stream: TcpStream, ssh: Arc<client::Handle<HostVerifier>>, host: &str, port: u16,
    resolved_hub_ips: &[IpAddr], credentials: &Credentials, activity: Arc<Mutex<Instant>>) -> Result<()> {
    let protocol = tokio::time::timeout(HANDSHAKE_TIMEOUT, proxy::handshake(&mut stream, host, port, resolved_hub_ips, credentials)).await.map_err(|_| "ssh_proxy_handshake")??;
    let channel = tokio::time::timeout(HANDSHAKE_TIMEOUT, ssh.channel_open_direct_tcpip(host, u32::from(port), "127.0.0.1", 0))
        .await.map_err(|_| "ssh_proxy_failed")?.map_err(|_| "ssh_proxy_failed")?;
    proxy::accepted(&mut stream, protocol).await?;
    let local_activity = Arc::new(Mutex::new(Instant::now()));
    let mut local = ActivityStream { inner: stream, activity: activity.clone(), local_activity: local_activity.clone() };
    let mut remote = ActivityStream { inner: channel.into_stream(), activity, local_activity: local_activity.clone() };
    // Tokio's duplex copier uses a fixed buffer per direction and propagates half-close/EOF.
    tokio::select! {
        result = tokio::io::copy_bidirectional(&mut local, &mut remote) => { result.map_err(|_| "ssh_proxy_failed")?; },
        _ = async { loop { tokio::time::sleep(Duration::from_secs(1)).await;
            if local_activity.lock().unwrap_or_else(|e| e.into_inner()).elapsed() >= IDLE_TIMEOUT { break; }
        } } => {},
    }
    Ok(())
}

struct ActivityStream<T> { inner: T, activity: Arc<Mutex<Instant>>, local_activity: Arc<Mutex<Instant>> }
impl<T: AsyncRead + Unpin> AsyncRead for ActivityStream<T> {
    fn poll_read(mut self: Pin<&mut Self>, cx: &mut Context<'_>, output: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        let before = output.filled().len(); let result = Pin::new(&mut self.inner).poll_read(cx, output);
        if output.filled().len() > before {
            *self.activity.lock().unwrap_or_else(|e| e.into_inner()) = Instant::now();
            *self.local_activity.lock().unwrap_or_else(|e| e.into_inner()) = Instant::now();
        }
        result
    }
}

// russh spawns its reader before connect_stream returns. Cancellation must therefore reach
// the owned stream, including while initial KEX has not yet produced a client Handle.
struct CancelableStream<T> { inner: T, cancelled: Pin<Box<tokio_util::sync::WaitForCancellationFutureOwned>> }
impl<T: AsyncRead + Unpin> AsyncRead for CancelableStream<T> {
    fn poll_read(mut self: Pin<&mut Self>, cx: &mut Context<'_>, buffer: &mut ReadBuf<'_>) -> Poll<io::Result<()>> {
        if self.cancelled.as_mut().poll(cx).is_ready() { return Poll::Ready(Err(io::ErrorKind::ConnectionAborted.into())); }
        Pin::new(&mut self.inner).poll_read(cx, buffer)
    }
}
impl<T: AsyncWrite + Unpin> AsyncWrite for CancelableStream<T> {
    fn poll_write(mut self: Pin<&mut Self>, cx: &mut Context<'_>, data: &[u8]) -> Poll<io::Result<usize>> {
        if self.cancelled.as_mut().poll(cx).is_ready() { return Poll::Ready(Err(io::ErrorKind::ConnectionAborted.into())); }
        Pin::new(&mut self.inner).poll_write(cx, data)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.cancelled.as_mut().poll(cx).is_ready() { return Poll::Ready(Err(io::ErrorKind::ConnectionAborted.into())); }
        Pin::new(&mut self.inner).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> { Pin::new(&mut self.inner).poll_shutdown(cx) }
}
impl<T: AsyncWrite + Unpin> AsyncWrite for ActivityStream<T> {
    fn poll_write(mut self: Pin<&mut Self>, cx: &mut Context<'_>, data: &[u8]) -> Poll<io::Result<usize>> { Pin::new(&mut self.inner).poll_write(cx, data) }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> { Pin::new(&mut self.inner).poll_flush(cx) }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> { Pin::new(&mut self.inner).poll_shutdown(cx) }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicUsize;
    use tokio::io::AsyncReadExt;

    struct AuthCounter(Arc<AtomicUsize>);
    impl russh::server::Handler for AuthCounter {
        type Error = russh::Error;
        async fn auth_none(&mut self, _user: &str) -> std::result::Result<russh::server::Auth, Self::Error> {
            self.0.fetch_add(1, Ordering::SeqCst); Ok(russh::server::Auth::reject())
        }
        async fn auth_publickey_offered(&mut self, _user: &str, _key: &russh::keys::PublicKey) -> std::result::Result<russh::server::Auth, Self::Error> {
            self.0.fetch_add(1, Ordering::SeqCst); Ok(russh::server::Auth::Accept)
        }
        async fn auth_publickey(&mut self, _user: &str, _key: &russh::keys::PublicKey) -> std::result::Result<russh::server::Auth, Self::Error> {
            self.0.fetch_add(1, Ordering::SeqCst); Ok(russh::server::Auth::Accept)
        }
    }
    struct ChannelTarget(Arc<Mutex<Option<(String, u32)>>>);
    impl russh::server::Handler for ChannelTarget {
        type Error = russh::Error;
        async fn auth_publickey(&mut self, _user: &str, _key: &russh::keys::PublicKey) -> std::result::Result<russh::server::Auth, Self::Error> {
            Ok(russh::server::Auth::Accept)
        }
        async fn channel_open_direct_tcpip(&mut self, _channel: russh::Channel<russh::server::Msg>, host: &str, port: u32,
            _originator_address: &str, _originator_port: u32, reply: russh::server::ChannelOpenHandle,
            _session: &mut russh::server::Session) -> std::result::Result<(), Self::Error> {
            *self.0.lock().unwrap() = Some((host.into(), port));
            reply.accept().await;
            Ok(())
        }
    }
    fn fixture_config(port: u16, pin: String) -> Config {
        let key = russh::keys::PrivateKey::random(&mut russh::keys::key::safe_rng(), russh::keys::Algorithm::Ed25519).unwrap();
        Config { ssh_host: "127.0.0.1".into(), ssh_port: port, user: "fixture".into(), host_key: pin,
            target_host: "hub.test".into(), target_port: 443, resolved_hub_ips: vec!["192.0.2.10".parse().unwrap()],
            private_key: Zeroizing::new(key.to_openssh(Default::default()).unwrap().as_bytes().to_vec()), passphrase: Zeroizing::new(vec![]) }
    }
    #[tokio::test] async fn wrong_pin_sends_zero_auth_requests_to_real_ssh_fixture() {
        let host_key = russh::keys::PrivateKey::random(&mut russh::keys::key::safe_rng(), russh::keys::Algorithm::Ed25519).unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap(); let port = listener.local_addr().unwrap().port();
        let count = Arc::new(AtomicUsize::new(0)); let server_count = count.clone();
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let config = russh::server::Config { keys: vec![host_key], auth_rejection_time: Duration::ZERO, ..Default::default() };
            if let Ok(session) = russh::server::run_stream(Arc::new(config), stream, AuthCounter(server_count)).await { let _ = session.await; }
        });
        let id = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"; begin(id).unwrap();
        let config = fixture_config(port, format!("SHA256:{}", STANDARD_NO_PAD.encode([0u8; 32])));
        let result = tokio::task::spawn_blocking(move || open(id, config)).await.unwrap();
        assert!(matches!(result, Err("ssh_host_key_mismatch")));
        tokio::time::timeout(Duration::from_secs(3), server).await.unwrap().unwrap();
        assert_eq!(count.load(Ordering::SeqCst), 0); assert!(!alive(id));
    }
    #[tokio::test] async fn captured_socks_ip_still_opens_the_original_logical_hub_channel() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let host_key = russh::keys::PrivateKey::random(&mut russh::keys::key::safe_rng(), russh::keys::Algorithm::Ed25519).unwrap();
        let pin = host_key.public_key().fingerprint(HashAlg::Sha256).to_string();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap(); let port = listener.local_addr().unwrap().port();
        let target = Arc::new(Mutex::new(None)); let server_target = target.clone();
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            let config = russh::server::Config { keys: vec![host_key], auth_rejection_time: Duration::ZERO, ..Default::default() };
            if let Ok(session) = russh::server::run_stream(Arc::new(config), stream, ChannelTarget(server_target)).await { let _ = session.await; }
        });
        let id = "dddddddd-dddd-dddd-dddd-dddddddddddd"; begin(id).unwrap();
        let config = fixture_config(port, pin);
        let lease = tokio::task::spawn_blocking(move || open(id, config)).await.unwrap().unwrap();
        let mut proxy = TcpStream::connect(("127.0.0.1", lease.port)).await.unwrap();
        proxy.write_all(&[5, 1, 2]).await.unwrap(); let mut selection = [0; 2]; proxy.read_exact(&mut selection).await.unwrap();
        assert_eq!(selection, [5, 2]);
        let mut auth = vec![1, lease.username.len() as u8]; auth.extend(lease.username.as_bytes()); auth.push(lease.password.len() as u8); auth.extend(lease.password.as_bytes());
        proxy.write_all(&auth).await.unwrap(); let mut auth_reply = [0; 2]; proxy.read_exact(&mut auth_reply).await.unwrap(); assert_eq!(auth_reply, [1, 0]);
        let mut request = vec![5, 1, 0, 1, 192, 0, 2, 10]; request.extend(443u16.to_be_bytes());
        proxy.write_all(&request).await.unwrap(); let mut connect_reply = [0; 10]; proxy.read_exact(&mut connect_reply).await.unwrap();
        assert_eq!(connect_reply[0..2], [5, 0]);
        assert_eq!(*target.lock().unwrap(), Some(("hub.test".into(), 443)));
        tokio::task::spawn_blocking(move || close(id)).await.unwrap();
        tokio::time::timeout(Duration::from_secs(3), server).await.unwrap().unwrap();
    }
    #[tokio::test] async fn cancelling_initial_key_exchange_closes_raw_socket() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap(); let port = listener.local_addr().unwrap().port();
        let id = "cccccccc-cccc-cccc-cccc-cccccccccccc"; begin(id).unwrap();
        let config = fixture_config(port, format!("SHA256:{}", STANDARD_NO_PAD.encode([0u8; 32])));
        let worker = tokio::task::spawn_blocking(move || open(id, config));
        let (mut socket, _) = tokio::time::timeout(Duration::from_secs(3), listener.accept()).await.unwrap().unwrap();
        let mut banner = [0u8; 128]; assert!(tokio::time::timeout(Duration::from_secs(3), socket.read(&mut banner)).await.unwrap().unwrap() > 0);
        cancel(id);
        let result = tokio::time::timeout(Duration::from_secs(3), worker).await.unwrap().unwrap();
        assert!(matches!(result, Err("ssh_cancelled")), "{:?}", result.as_ref().err());
        let mut tail = Vec::new(); tokio::time::timeout(Duration::from_secs(3), socket.read_to_end(&mut tail)).await.unwrap().unwrap();
        assert!(!alive(id));
    }
    #[tokio::test] async fn host_verifier_rejects_wrong_key_before_any_authentication() {
        let key = russh::keys::PrivateKey::random(&mut russh::keys::key::safe_rng(), russh::keys::Algorithm::Ed25519).unwrap();
        let public = key.public_key().clone(); let rejected = Arc::new(AtomicBool::new(false));
        let mut verifier = HostVerifier { pin: format!("SHA256:{}", STANDARD_NO_PAD.encode([0u8; 32])), rejected: rejected.clone() };
        assert!(!client::Handler::check_server_key(&mut verifier, &public.clone().into()).await.unwrap());
        assert!(rejected.load(Ordering::SeqCst));
        verifier.pin = public.fingerprint(HashAlg::Sha256).to_string();
        assert!(client::Handler::check_server_key(&mut verifier, &public.into()).await.unwrap());
    }
    #[test] fn cancellation_before_worker_start_prevents_connection() {
        let id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"; begin(id).unwrap(); close(id); assert!(!alive(id));
        let config = Config { ssh_host: "invalid.test".into(), ssh_port: 22, user: "user".into(), host_key: String::new(),
            target_host: "hub.test".into(), target_port: 443, resolved_hub_ips: vec!["192.0.2.10".parse().unwrap()],
            private_key: Zeroizing::new(vec![]), passphrase: Zeroizing::new(vec![]) };
        assert!(matches!(open(id, config), Err("ssh_cancelled"))); close(id);
    }
    #[test] fn pin_format_is_sha256_only() {
        assert!(valid_pin(&format!("SHA256:{}", STANDARD_NO_PAD.encode([1u8; 32]))));
        for pin in ["", "MD5:aa:bb", "SHA256:invalid", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="] { assert!(!valid_pin(pin)); }
    }
    #[test] fn unsupported_unbounded_kdf_formats_fail_without_attempting_decryption() {
        for text in ["PuTTY-User-Key-File-3: ssh-ed25519\n", "-----BEGIN ENCRYPTED PRIVATE KEY-----\n"] {
            assert!(matches!(decode_key(text, "passphrase"), Err("ssh_invalid_key")));
        }
    }
}
