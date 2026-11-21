//! The phone over RemotePairing: verify the pairing record, open the encrypted tunnel with a userspace TCP
//! stack (no interface, route or DNS change on this computer), read the service directory (RSD), then run
//! operations on the developer services it lists.
//!
//! Not verified on a real device. Everything here follows the library's own descriptions of the services.

use std::net::{IpAddr, SocketAddr};
use std::path::Path;
use std::time::Duration;

use idevice::core_device::{
    AppServiceClient, ButtonState, CallInfoBlob, DisplayServiceClient, ImageFormat as DeviceImageFormat,
    IndigoHidClient, ScreenCaptureServiceClient, TOUCHSCREEN_STATE_CONTACT, TOUCHSCREEN_STATE_RELEASE,
    UniversalHidServiceClient, build_screen_audio_offer, build_screen_video_offer, build_start_audio_parameters,
    build_start_video_parameters,
};
use idevice::cryptexd::{self, Cryptex1Assets, CryptexInstallRequest, CryptexdClient};
use idevice::dvt::device_info::DeviceInfoClient;
use idevice::dvt::message::AuxValue;
use idevice::dvt::remote_server::RemoteServerClient;
use idevice::installation_proxy::InstallationProxyClient;
use idevice::lockdown::LockdownClient;
use idevice::mobile_image_mounter::ImageMounter;
use idevice::remote_pairing::{RemotePairingClient, RpPairingSocket, connect_tls_psk_tunnel_native};
use idevice::rsd::RsdHandshake;
use idevice::springboardservices::{InterfaceOrientation, SpringBoardServicesClient};
use idevice::tcp::adapter::Adapter;
use idevice::tcp::handle::AdapterHandle;
use idevice::tss::TSSRequest;
use idevice::{IdeviceError, ReadWrite, RsdService};
use serde_json::{Map, Value, json};
use tokio::net::TcpStream;
use tokio::task::JoinHandle;

use crate::error::{BridgeError, Code, Phase, from_idevice};
use crate::geometry::{self, Orientation, Point, Screen};
use crate::keys::{LEFT_SHIFT, Stroke};
use crate::ops::{Action, AppTarget, KeyInput, ShotOptions, SwipeSpec};
use crate::record::Record;
use crate::run::Phone;
use crate::shot;

/// How long the input gate stays open after the last input, so a run of taps doesn't reopen it each time.
const GATE_IDLE: Duration = Duration::from_secs(10);
/// After the gate opens, the phone needs a moment before input counts as real (the library's description).
const GATE_SETTLE: Duration = Duration::from_millis(300);
/// The display stream stays at least this long after the last input before it stops, so the phone acts on it.
const INPUT_LINGER: Duration = Duration::from_secs(1);
/// After input, before answering: one-way key and button events get this to be read on the phone.
const KEY_SETTLE: Duration = Duration::from_millis(100);
/// How long a finger rests for a tap, and between the taps of a double tap.
const TAP_HOLD: Duration = Duration::from_millis(50);
const TAP_GAP: Duration = Duration::from_millis(80);
const KEY_GAP: Duration = Duration::from_millis(15);
/// What this computer says it is when opening the display stream. Values the phone accepts from a Mac.
const CLIENT_SUPPORTED_FEATURES: u64 = 140;
const DEVELOPER_MODE: (&str, Option<&str>) = ("DeveloperModeStatus", Some("com.apple.security.mac.amfi"));
/// Stopping the display stream when idle must not hang `serve` on a link that has gone quiet.
const STOP_TIMEOUT: Duration = Duration::from_secs(3);
/// A service directory read (or tried) this recently is current: `device_info` doesn't read it again.
const SERVICES_FRESH: Duration = Duration::from_secs(5);
/// Reading the service directory again must not hold up `device_info` on a link that has gone quiet.
const SERVICES_REREAD_TIMEOUT: Duration = Duration::from_secs(5);
/// cryptexd answers a lookup in well under a second. One that doesn't (seen on iOS 27.2.1: every routine hung while an
/// install sent to the locked phone was pending) must not hold up the request, nor `device_info` and the node's
/// health check behind it.
const CRYPTEXD_TIMEOUT: Duration = Duration::from_secs(5);

/// cryptexd answers one routine per connection.
type Cryptexd = CryptexdClient<Box<dyn ReadWrite>>;

/// A cryptexd lookup that must answer within CRYPTEXD_TIMEOUT. A phone that doesn't is most likely locked.
async fn cryptexd_within<T>(
    what: &str,
    lookup: impl Future<Output = Result<T, IdeviceError>>,
) -> Result<T, BridgeError> {
    match tokio::time::timeout(CRYPTEXD_TIMEOUT, lookup).await {
        Ok(answer) => answer.map_err(|e| from_idevice(e, Phase::Operation, what)),
        Err(_) => Err(BridgeError::new(
            Code::Locked,
            format!(
                "{what}: cryptexd didn't answer within {} s, as on a locked phone: unlock it, keep it awake and try again",
                CRYPTEXD_TIMEOUT.as_secs()
            ),
        )),
    }
}

/// Where to reach the phone.
pub struct Target {
    pub udid: String,
    pub host: String,
    pub port: u16,
}

pub struct Device {
    udid: String,
    adapter: AdapterHandle,
    rsd: RsdHandshake,
    /// The RemotePairing control connection: kept for as long as the tunnel it opened.
    _control: RemotePairingClient<RpPairingSocket<TcpStream>>,
    screen: Option<Screen>,
    hid: Option<UniversalHidServiceClient<Box<dyn ReadWrite>>>,
    indigo: Option<IndigoHidClient<Box<dyn ReadWrite>>>,
    gate: Option<Gate>,
    apps: Option<AppServiceClient<Box<dyn ReadWrite>>>,
    springboard: Option<SpringBoardServicesClient>,
    /// Where the service directory answers inside the tunnel, to read it again after a mount.
    rsd_port: u16,
    /// When the service directory was last read or tried (tokio's clock, so tests can move it).
    services_checked: tokio::time::Instant,
    /// The iOS major version, read once.
    ios_major: Option<u32>,
    /// The developer services came with a disk image this session can't list: it must be started afresh.
    stale: bool,
}

/// The phone takes synthetic input only while a display stream is running; its video is read and dropped.
struct Gate {
    client: DisplayServiceClient<Box<dyn ReadWrite>>,
    drains: Vec<JoinHandle<()>>,
    /// When the last input went out (tokio's clock, so tests can move it).
    last_used: tokio::time::Instant,
}

/// Resolves `host` (an IP literal, an IPv6 literal with `%zone`, or a name) and `port`.
async fn socket_addr(host: &str, port: u16) -> Result<SocketAddr, BridgeError> {
    let unreachable = |why: String| BridgeError::new(Code::Unreachable, format!("{host}: {why}"));
    let bare = host.trim_start_matches('[').trim_end_matches(']');
    if let Some((ip, zone)) = bare.split_once('%') {
        let ip: std::net::Ipv6Addr = ip.parse().map_err(|_| unreachable("not an IPv6 address".into()))?;
        let scope = zone_index(zone).ok_or_else(|| unreachable(format!("unknown interface {zone:?}")))?;
        return Ok(SocketAddr::V6(std::net::SocketAddrV6::new(ip, port, 0, scope)));
    }
    if let Ok(ip) = bare.parse::<IpAddr>() {
        return Ok(SocketAddr::new(ip, port));
    }
    tokio::net::lookup_host((bare, port))
        .await
        .map_err(|e| unreachable(e.to_string()))?
        .next()
        .ok_or_else(|| unreachable("no address".into()))
}

fn zone_index(zone: &str) -> Option<u32> {
    if let Ok(n) = zone.parse() {
        return Some(n);
    }
    #[cfg(unix)]
    {
        let name = std::ffi::CString::new(zone).ok()?;
        // SAFETY: a valid NUL-terminated string; the call only reads it.
        let index = unsafe { libc::if_nametoindex(name.as_ptr()) };
        if index != 0 {
            return Some(index);
        }
    }
    None
}

async fn dial(addr: SocketAddr, what: &str) -> Result<TcpStream, BridgeError> {
    let s = TcpStream::connect(addr)
        .await
        .map_err(|e| BridgeError::new(Code::Unreachable, format!("{what} {addr}: {e}")))?;
    let _ = s.set_nodelay(true);
    Ok(s)
}

impl Device {
    pub async fn connect(target: &Target, mut record: Record) -> Result<Self, BridgeError> {
        let addr = socket_addr(&target.host, target.port).await?;

        // Prove who we are with the saved record. Never fall back to pairing anew: that needs the user.
        let stream = dial(addr, "connect to").await?;
        let mut control = RemotePairingClient::new(RpPairingSocket::new(stream), &record.sending_host);
        control
            .attempt_pair_verify()
            .await
            .map_err(|e| from_idevice(e, Phase::Pairing, "RemotePairing handshake"))?;
        control
            .validate_pairing(&mut record.pairing)
            .await
            .map_err(|e| from_idevice(e, Phase::Pairing, "verify pairing record"))?;

        // The encrypted tunnel, on a port the phone opens for it.
        let port = control
            .create_tcp_listener()
            .await
            .map_err(|e| from_idevice(e, Phase::Tunnel, "open tunnel listener"))?;
        let mut tunnel_addr = addr; // keeps an IPv6 zone
        tunnel_addr.set_port(port);
        let stream = dial(tunnel_addr, "connect tunnel").await?;
        let tunnel = connect_tls_psk_tunnel_native(stream, control.encryption_key())
            .await
            .map_err(|e| from_idevice(e, Phase::Tunnel, "tunnel handshake"))?;
        let info = tunnel.info.clone();
        let parse_ip = |s: &str| {
            s.parse::<IpAddr>()
                .map_err(|_| BridgeError::new(Code::TunnelFailed, format!("tunnel address {s:?}")))
        };
        let mut adapter = Adapter::new(
            Box::new(tunnel.into_inner()),
            parse_ip(&info.client_address)?,
            parse_ip(&info.server_address)?,
        );
        adapter.set_mss(usize::from(info.mtu).saturating_sub(60));
        let mut adapter = adapter.to_async_handle();

        let rsd_stream = adapter
            .connect(info.server_rsd_port)
            .await
            .map_err(|e| from_idevice(e.into(), Phase::Tunnel, "connect service directory"))?;
        let rsd = RsdHandshake::new(rsd_stream)
            .await
            .map_err(|e| from_idevice(e, Phase::Tunnel, "service directory handshake"))?;

        let mut device = Self {
            udid: String::new(),
            adapter,
            rsd,
            _control: control,
            screen: None,
            hid: None,
            indigo: None,
            gate: None,
            apps: None,
            springboard: None,
            rsd_port: info.server_rsd_port,
            services_checked: tokio::time::Instant::now(),
            ios_major: None,
            stale: false,
        };
        let udid = device.udid_reported().await?;
        if !udid.eq_ignore_ascii_case(&target.udid) {
            return Err(BridgeError::new(
                Code::WrongDevice,
                format!("the phone at {} is {udid}, not {}", target.host, target.udid),
            ));
        }
        device.udid = udid;
        Ok(device)
    }

    async fn udid_reported(&mut self) -> Result<String, BridgeError> {
        if let Some(u) = self
            .rsd
            .properties
            .get("UniqueDeviceID")
            .and_then(plist::Value::as_string)
        {
            return Ok(u.to_owned());
        }
        let [v] = self.lockdown_values([("UniqueDeviceID", None)]).await;
        v.as_ref()
            .and_then(plist::Value::as_string)
            .map(str::to_owned)
            .ok_or_else(|| BridgeError::new(Code::TunnelFailed, "the phone did not say which it is"))
    }

    fn has_service<T: RsdService>(&self) -> bool {
        self.rsd.services.contains_key(T::rsd_service_name().as_ref())
    }

    async fn connect_service<T: RsdService>(&mut self, what: &str) -> Result<T, BridgeError> {
        if !self.has_service::<T>() {
            return Err(self.missing_service(what).await);
        }
        T::connect_rsd(&mut self.adapter, &mut self.rsd)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, what))
    }

    /// Why a developer service is not listed: Developer Mode off, or the developer disk image not mounted.
    async fn missing_service(&mut self, what: &str) -> BridgeError {
        if self.developer_mode().await == Some(false) {
            BridgeError::new(
                Code::DeveloperModeOff,
                format!("{what}: Developer Mode is off on the phone"),
            )
        } else {
            BridgeError::new(
                Code::DdiNotMounted,
                format!("{what}: the phone does not offer it; the developer disk image is probably not mounted"),
            )
        }
    }

    /// Lockdown values `(key, domain)` over one connection, `None` for each that cannot be read.
    async fn lockdown_values<const N: usize>(&mut self, keys: [(&str, Option<&str>); N]) -> [Option<plist::Value>; N] {
        let mut out = [const { None }; N];
        let Ok(mut lockdown) = LockdownClient::connect_rsd(&mut self.adapter, &mut self.rsd).await else {
            return out;
        };
        for (slot, (key, domain)) in out.iter_mut().zip(keys) {
            *slot = lockdown.get_value(Some(key), domain).await.ok();
        }
        out
    }

    async fn ios_major(&mut self) -> Option<u32> {
        if self.ios_major.is_none() {
            let [v] = self.lockdown_values([("ProductVersion", None)]).await;
            self.ios_major = v.as_ref().and_then(plist::Value::as_string).and_then(major_version);
        }
        self.ios_major
    }

    async fn developer_mode(&mut self) -> Option<bool> {
        let [v] = self.lockdown_values([DEVELOPER_MODE]).await;
        v.and_then(|v| v.as_boolean())
    }

    /// The interface orientation, portrait when it cannot be read.
    async fn orientation(&mut self) -> Orientation {
        if self.springboard.is_none() {
            self.springboard = SpringBoardServicesClient::connect_rsd(&mut self.adapter, &mut self.rsd)
                .await
                .ok();
        }
        let Some(sb) = self.springboard.as_mut() else {
            return Orientation::Portrait;
        };
        match sb.get_interface_orientation().await {
            Ok(InterfaceOrientation::PortraitUpsideDown) => Orientation::PortraitUpsideDown,
            Ok(InterfaceOrientation::LandscapeRight) => Orientation::LandscapeRight,
            Ok(InterfaceOrientation::LandscapeLeft) => Orientation::LandscapeLeft,
            Ok(_) => Orientation::Portrait,
            Err(_) => {
                self.springboard = None;
                Orientation::Portrait
            }
        }
    }

    /// The panel's size and scale: from the phone's own description, else from a screenshot.
    async fn screen(&mut self) -> Result<Screen, BridgeError> {
        if let Some(s) = self.screen {
            return Ok(s);
        }
        const DOMAIN: Option<&str> = Some("com.apple.mobile.iTunes");
        let num =
            |v: Option<plist::Value>| v.and_then(|v| v.as_real().or_else(|| v.as_unsigned_integer().map(|n| n as f64)));
        let [w, h, scale] = self
            .lockdown_values([
                ("ScreenWidth", DOMAIN),
                ("ScreenHeight", DOMAIN),
                ("ScreenScaleFactor", DOMAIN),
            ])
            .await;
        let screen = match (num(w), num(h), num(scale)) {
            (Some(w), Some(h), Some(scale)) if w > 0.0 && h > 0.0 && scale > 0.0 => Screen {
                pixel_width: w.min(h) as u32,
                pixel_height: w.max(h) as u32,
                scale,
            },
            _ => {
                let png = self.capture(DeviceImageFormat::Png).await?;
                let (w, h) = shot::dimensions(&png)?;
                shot::screen_from_capture(w, h)
            }
        };
        self.screen = Some(screen);
        Ok(screen)
    }

    async fn capture(&mut self, format: DeviceImageFormat) -> Result<Vec<u8>, BridgeError> {
        let mut client: ScreenCaptureServiceClient<Box<dyn ReadWrite>> = self.connect_service("screenshot").await?;
        client
            .take_screenshot(None, format)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "screenshot"))
    }

    async fn device_info(&mut self) -> Result<Value, BridgeError> {
        let text = |v: Option<plist::Value>| v.and_then(|v| v.as_string().map(str::to_owned));
        const BATTERY: Option<&str> = Some("com.apple.mobile.battery");
        let [name, product_type, os_version, build, developer_mode, level, charging] = self
            .lockdown_values([
                ("DeviceName", None),
                ("ProductType", None),
                ("ProductVersion", None),
                ("BuildVersion", None),
                DEVELOPER_MODE,
                ("BatteryCurrentCapacity", BATTERY),
                ("BatteryIsCharging", BATTERY),
            ])
            .await;
        let (name, product_type, os_version, build) = (text(name), text(product_type), text(os_version), text(build));
        let developer_mode = developer_mode.and_then(|v| v.as_boolean());
        let level = level.and_then(|v| v.as_unsigned_integer());
        let charging = charging.and_then(|v| v.as_boolean());
        // The developer services are listed only while the disk image is mounted, and a session reads the directory
        // when it connects. One mounted since (by Xcode, or by another session's mount_ddi) needs a fresh read. A
        // cryptex install resets the phone's service directory connections (seen on iOS 27.2.1: Xcode's was torn down
        // when this bridge installed the image), and a directory that can't be read again, or still doesn't list the
        // services although the phone has the image, means this session can't reach them: connect again.
        if developer_mode != Some(false)
            && !self.lists_developer_services()
            && self.services_checked.elapsed() >= SERVICES_FRESH
        {
            let reason = if !self.read_services_again().await {
                Some("the phone's service directory can't be read again (a disk image mounted since resets it)")
            } else if !self.lists_developer_services()
                && matches!(
                    tokio::time::timeout(CRYPTEXD_TIMEOUT, crate::ddi::Mounter::mounted(self)).await,
                    Ok(Ok(true))
                )
            {
                Some(
                    "the developer disk image was mounted after this session connected and its services aren't listed here",
                )
            } else {
                None
            };
            if let Some(reason) = reason {
                self.stale = true;
                return Err(BridgeError::new(Code::Disconnected, format!("{reason}: connect again")));
            }
        }
        let orientation = self.orientation().await;
        let screen = self.screen().await.ok();
        let frame = screen.map(|s| s.frame(orientation));

        let screenshot = self.has_service::<ScreenCaptureServiceClient<Box<dyn ReadWrite>>>();
        let input_services = self.has_service::<UniversalHidServiceClient<Box<dyn ReadWrite>>>()
            && self.has_service::<IndigoHidClient<Box<dyn ReadWrite>>>()
            && self.has_service::<DisplayServiceClient<Box<dyn ReadWrite>>>();
        let old_ios = self.ios_major().await.is_some_and(|m| !input_supported(m));
        let input = input_services && !old_ios;
        let apps = self.has_service::<AppServiceClient<Box<dyn ReadWrite>>>()
            && self.has_service::<RemoteServerClient<Box<dyn ReadWrite>>>();
        let missing = if developer_mode == Some(false) {
            Code::DeveloperModeOff
        } else {
            Code::DdiNotMounted
        };
        let mut notes = Map::new();
        if input_services && old_ios {
            notes.insert("input".into(), json!(Code::Unsupported.as_str()));
        }
        for (cap, ok) in [("screenshot", screenshot), ("input", input_services), ("apps", apps)] {
            if !ok {
                notes.insert(cap.into(), json!(missing.as_str()));
            }
        }
        notes.insert("ax_tree".into(), json!(Code::Unsupported.as_str()));

        Ok(json!({
            "udid": self.udid,
            "name": name,
            "product_type": product_type,
            "os_version": os_version,
            "build": build,
            "point_width": frame.map(|f| f.0),
            "point_height": frame.map(|f| f.1),
            "scale": screen.map(|s| s.scale),
            "orientation": orientation.as_str(),
            "locked": Value::Null,
            "developer_mode": developer_mode,
            // With Developer Mode off nothing is listed, whether the image is mounted or not.
            "ddi_mounted": if developer_mode == Some(false) && !(input || screenshot) {
                None
            } else {
                Some(input || screenshot)
            },
            "battery": level.map(|l| json!({"level": l as f64 / 100.0, "charging": charging})),
            "simulated": false,
            "transport": "network",
            "capabilities": {"screenshot": screenshot, "ax_tree": false, "input": input, "apps": apps},
            "notes": notes,
        }))
    }

    async fn screenshot(&mut self, opts: ShotOptions) -> Result<Value, BridgeError> {
        let orientation = self.orientation().await;
        let screen = self.screen().await?;
        // Always PNG from the phone: iOS 18 refuses "jpeg"; the bridge converts.
        let started = tokio::time::Instant::now();
        let raw = self.capture(DeviceImageFormat::Png).await?;
        let captured = started.elapsed();
        let image = shot::prepare(raw, &opts, orientation)?;
        let converted = started.elapsed() - captured;
        let (pw, ph) = screen.frame(orientation);
        let mut out = json!({
            "mime": image.mime,
            "width": image.width,
            "height": image.height,
            "point_width": pw,
            "point_height": ph,
            "orientation": orientation.as_str(),
            // Where the time went, to find out why a capture is sometimes slow (#3792).
            "timing_ms": {
                "capture": captured.as_millis() as u64,
                "convert": converted.as_millis() as u64,
                "stream_open": self.gate.is_some(),
            },
        });
        match &opts.out_file {
            Some(path) => crate::files::write_private(Path::new(path), &image.bytes)?,
            None => {
                use base64::Engine as _;
                out["data"] = json!(base64::engine::general_purpose::STANDARD.encode(&image.bytes));
            }
        }
        Ok(out)
    }

    /// Opens the input gate if it is not open, and the input services.
    async fn ready_for_input(&mut self) -> Result<(), BridgeError> {
        if let Some(major) = self.ios_major().await
            && !input_supported(major)
        {
            return Err(input_needs_newer_ios(major));
        }
        if self.gate.is_none() {
            let gate = self.open_gate().await?;
            self.gate = Some(gate);
            tokio::time::sleep(GATE_SETTLE).await;
        }
        if self.hid.is_none() {
            self.hid = Some(self.connect_service("touch input").await?);
        }
        if self.indigo.is_none() {
            self.indigo = Some(self.connect_service("key input").await?);
        }
        Ok(())
    }

    async fn open_gate(&mut self) -> Result<Gate, BridgeError> {
        let mut client: DisplayServiceClient<Box<dyn ReadWrite>> = self.connect_service("display stream").await?;
        let fail =
            |what: &str, e: String| BridgeError::new(Code::ServiceUnavailable, format!("display stream: {what}: {e}"));
        let audio = self
            .adapter
            .bind_udp(0)
            .await
            .map_err(|e| fail("bind", e.to_string()))?;
        let video = self
            .adapter
            .bind_udp(0)
            .await
            .map_err(|e| fail("bind", e.to_string()))?;
        let receiver_ip = self.adapter.host_ip().to_string();
        let sender_ip = self.adapter.peer_ip().to_string();
        let call_info = CallInfoBlob {
            call_id: 0,
            client_version: 1,
            device_type: "Mac17,7".into(),
            framework_version: "2205.3.1".into(),
            os_version: "25F71".into(),
            device_name: None,
            audio_device_uid: None,
        };
        let session = uuid::Uuid::new_v4();
        let call_id = || uuid::Uuid::new_v4().to_string().to_uppercase();
        // Audio first: it sets up the screen-sharing session the video stream joins.
        let offer = build_screen_audio_offer(&call_id(), &call_info).map_err(|e| fail("audio offer", e.to_string()))?;
        let params = build_start_audio_parameters(
            &receiver_ip,
            audio.local_port(),
            &sender_ip,
            50000,
            offer,
            CLIENT_SUPPORTED_FEATURES,
            session,
        );
        client
            .start_media_stream(params)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "start audio stream"))?;
        let ssrc = uuid::Uuid::new_v4().as_u128() as u32;
        let offer =
            build_screen_video_offer(&call_id(), &call_info, ssrc).map_err(|e| fail("video offer", e.to_string()))?;
        let params = build_start_video_parameters(
            &receiver_ip,
            video.local_port(),
            &sender_ip,
            50001,
            offer,
            CLIENT_SUPPORTED_FEATURES,
            1,
            session,
        );
        if let Err(e) = client.start_media_stream(params).await {
            let _ = client.stop_media_stream().await;
            return Err(from_idevice(e, Phase::Operation, "start video stream"));
        }
        // Read and drop what the phone streams, so nothing piles up in memory.
        let drains = [audio, video]
            .into_iter()
            .map(|socket| tokio::spawn(async move { while socket.recv().await.is_ok() {} }))
            .collect();
        Ok(Gate {
            client,
            drains,
            last_used: tokio::time::Instant::now(),
        })
    }

    /// Stops the display stream; with `link_lost`, only lets go of it, since nothing can reach the phone. The
    /// phone takes input only while the stream runs, so it stops no sooner than `INPUT_LINGER` after the last
    /// input: stopping right away (a one-shot call, or `serve` ending) dropped a tap the phone hadn't acted on.
    async fn close_gate(&mut self, link_lost: bool) {
        if let Some(mut gate) = self.gate.take() {
            if !link_lost {
                let client = &mut gate.client;
                stop_after_linger(gate.last_used, async {
                    let _ = tokio::time::timeout(STOP_TIMEOUT, client.stop_media_stream()).await;
                })
                .await;
            }
            for d in gate.drains {
                d.abort();
            }
        }
    }

    /// Waits until the phone has read the input sent so far before the answer goes back. The touch service
    /// answers requests in order on the connection that carried the touches, so one round trip there means the
    /// phone has them; keys and buttons go one way on their own connection, so they get `KEY_SETTLE`.
    async fn confirm_input(&mut self) -> Result<(), BridgeError> {
        if let Some(hid) = self.hid.as_mut() {
            hid.list_connected_services()
                .await
                .map_err(|e| from_idevice(e, Phase::Operation, "confirm input"))?;
        }
        tokio::time::sleep(KEY_SETTLE).await;
        Ok(())
    }

    async fn touch(&mut self, x: u16, y: u16, down: bool) -> Result<(), BridgeError> {
        let hid = self.hid.as_mut().expect("ready_for_input connected it");
        let state = if down {
            TOUCHSCREEN_STATE_CONTACT
        } else {
            TOUCHSCREEN_STATE_RELEASE
        };
        hid.send_touchscreen(state, x, y, None)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "touch"))
    }

    /// Puts a finger down at the first point, moves it through the rest over `move_ms`, lifts it at the last.
    async fn stroke_path(&mut self, points: &[(u16, u16)], hold: Duration, move_ms: u64) -> Result<(), BridgeError> {
        let (&first, rest) = points.split_first().expect("a path has points");
        self.touch(first.0, first.1, true).await?;
        tokio::time::sleep(hold).await;
        let step = Duration::from_millis(move_ms / rest.len().max(1) as u64);
        for &(x, y) in rest {
            self.touch(x, y, true).await?;
            tokio::time::sleep(step).await;
        }
        let last = points[points.len() - 1];
        self.touch(last.0, last.1, false).await
    }

    async fn gesture(&mut self, action: Action) -> Result<(), BridgeError> {
        let orientation = self.orientation().await;
        let screen = self.screen().await?;
        let map = |p: Point| geometry::to_touch(p, &screen, orientation);
        let to_path = |a: Point, b: Point, ms: u64| -> Result<Vec<(u16, u16)>, BridgeError> {
            let mut pts = vec![map(a)?];
            for p in geometry::path(a, b, ms) {
                pts.push(map(p)?);
            }
            Ok(pts)
        };
        // Every point is checked before the finger goes down, so a bad one never leaves half a gesture.
        enum Plan {
            Taps((u16, u16), u32),
            Press((u16, u16), u64),
            Path(Vec<(u16, u16)>, Duration, u64),
        }
        let plan = match action {
            Action::Tap { at, count } => Plan::Taps(map(at)?, count),
            Action::LongPress { at, duration_ms } => Plan::Press(map(at)?, duration_ms),
            Action::Drag {
                from,
                to,
                duration_ms,
                hold_ms,
            } => Plan::Path(
                to_path(from, to, duration_ms)?,
                Duration::from_millis(hold_ms.max(16)),
                duration_ms,
            ),
            Action::Swipe { spec, duration_ms } => {
                let (a, b) = match spec {
                    SwipeSpec::Ends(a, b) => (a, b),
                    SwipeSpec::Direction { dir, origin, distance } => {
                        geometry::swipe_ends(dir, origin, distance, screen.frame(orientation))
                    }
                };
                Plan::Path(to_path(a, b, duration_ms)?, Duration::from_millis(16), duration_ms)
            }
            _ => unreachable!("only gestures come here"),
        };
        self.ready_for_input().await?;
        match plan {
            Plan::Taps((x, y), count) => {
                for i in 0..count {
                    if i > 0 {
                        tokio::time::sleep(TAP_GAP).await;
                    }
                    self.touch(x, y, true).await?;
                    tokio::time::sleep(TAP_HOLD).await;
                    self.touch(x, y, false).await?;
                }
                Ok(())
            }
            Plan::Press((x, y), ms) => {
                self.touch(x, y, true).await?;
                tokio::time::sleep(Duration::from_millis(ms)).await;
                self.touch(x, y, false).await
            }
            Plan::Path(points, hold, ms) => self.stroke_path(&points, hold, ms).await,
        }
    }

    async fn key(&mut self, usage: u64, down: bool) -> Result<(), BridgeError> {
        let indigo = self.indigo.as_mut().expect("ready_for_input connected it");
        let state = if down { ButtonState::Down } else { ButtonState::Up };
        indigo
            .send_keyboard(usage, state)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "key"))
    }

    async fn type_strokes(&mut self, strokes: &[Stroke]) -> Result<(), BridgeError> {
        for s in strokes {
            if s.shift {
                self.key(LEFT_SHIFT, true).await?;
            }
            self.key(s.usage, true).await?;
            self.key(s.usage, false).await?;
            if s.shift {
                self.key(LEFT_SHIFT, false).await?;
            }
            tokio::time::sleep(KEY_GAP).await;
        }
        Ok(())
    }

    async fn keys(&mut self, input: KeyInput) -> Result<(), BridgeError> {
        // Only ASCII can be typed as key presses: refuse anything else before a key goes down.
        let strokes = match &input {
            KeyInput::Text(text) => Some(crate::keys::strokes(text)?),
            KeyInput::Key { .. } => None,
        };
        self.ready_for_input().await?;
        match input {
            KeyInput::Text(_) => self.type_strokes(&strokes.unwrap_or_default()).await,
            KeyInput::Key { usage, repeat, .. } => {
                let one = [Stroke { usage, shift: false }];
                for _ in 0..repeat {
                    self.type_strokes(&one).await?;
                }
                Ok(())
            }
        }
    }

    async fn button(&mut self, button: crate::keys::Button, hold_ms: u64) -> Result<(), BridgeError> {
        self.ready_for_input().await?;
        let indigo = self.indigo.as_mut().expect("ready_for_input connected it");
        let fail = |e| from_idevice(e, Phase::Operation, "button");
        indigo
            .send_button(button.usage_page, button.usage, ButtonState::Down)
            .await
            .map_err(fail)?;
        tokio::time::sleep(Duration::from_millis(hold_ms)).await;
        indigo
            .send_button(button.usage_page, button.usage, ButtonState::Up)
            .await
            .map_err(fail)
    }

    async fn app_service(&mut self) -> Result<&mut AppServiceClient<Box<dyn ReadWrite>>, BridgeError> {
        if self.apps.is_none() {
            self.apps = Some(self.connect_service("app service").await?);
        }
        Ok(self.apps.as_mut().expect("just set"))
    }

    /// Runs an app-service call; drops the cached client on failure, since its stream may be out of step.
    async fn with_apps<T>(
        &mut self,
        what: &str,
        f: impl AsyncFnOnce(&mut AppServiceClient<Box<dyn ReadWrite>>) -> Result<T, IdeviceError>,
    ) -> Result<T, BridgeError> {
        let apps = self.app_service().await?;
        let result = f(apps).await;
        if result.is_err() {
            self.apps = None;
        }
        result.map_err(|e| from_idevice(e, Phase::Operation, what))
    }

    /// Installed apps, from the installation service: CoreDevice's app list never answered on iOS 18.
    async fn installed(&mut self) -> Result<Vec<InstalledApp>, BridgeError> {
        let work = async {
            let mut proxy = InstallationProxyClient::connect_rsd(&mut self.adapter, &mut self.rsd)
                .await
                .map_err(|e| from_idevice(e, Phase::Operation, "installation service"))?;
            let options = plist::Value::Dictionary(
                [
                    ("ApplicationType".to_owned(), plist::Value::String("Any".into())),
                    (
                        "ReturnAttributes".to_owned(),
                        plist::Value::Array(
                            BROWSE_ATTRIBUTES
                                .iter()
                                .map(|a| plist::Value::String((*a).into()))
                                .collect(),
                        ),
                    ),
                ]
                .into_iter()
                .collect(),
            );
            proxy
                .browse(Some(options))
                .await
                .map_err(|e| from_idevice(e, Phase::Operation, "list apps"))
        };
        Ok(parse_browse(answer_within(work, "list apps").await??))
    }

    async fn list_apps(&mut self, include_system: bool) -> Result<Value, BridgeError> {
        let apps: Vec<Value> = self
            .installed()
            .await?
            .into_iter()
            .filter(|a| include_system || !a.system)
            .map(|a| {
                json!({
                    "bundle_id": a.bundle_id,
                    "name": a.name,
                    "version": a.version,
                    "type": if a.system { "system" } else { "user" },
                })
            })
            .collect();
        Ok(json!({ "apps": apps }))
    }

    async fn launch_app(
        &mut self,
        bundle_id: String,
        args: Vec<String>,
        env: Vec<(String, String)>,
        kill_existing: bool,
    ) -> Result<Value, BridgeError> {
        let env: plist::Dictionary = env.into_iter().map(|(k, v)| (k, plist::Value::String(v))).collect();
        let id = bundle_id.clone();
        let launched = self
            .with_apps("launch app", async move |a| {
                let args: Vec<&str> = args.iter().map(String::as_str).collect();
                a.launch_application(id, &args, kill_existing, false, Some(env), None, None)
                    .await
            })
            .await;
        match launched {
            Ok(r) => Ok(json!({"bundle_id": bundle_id, "pid": r.pid})),
            Err(e) => match self.installed().await {
                Ok(apps) if !apps.iter().any(|a| a.bundle_id == bundle_id) => Err(BridgeError::new(
                    Code::AppNotFound,
                    format!("no app {bundle_id} on the phone"),
                )),
                _ => Err(e),
            },
        }
    }

    /// Ends an app through the instruments service (it lists processes with their executables and kills by
    /// pid): CoreDevice's process list never answered on iOS 18. Only an app process is ever killed.
    async fn terminate_app(&mut self, target: AppTarget) -> Result<Value, BridgeError> {
        let bundle_path = match &target {
            AppTarget::BundleId(id) => Some(
                self.installed()
                    .await?
                    .into_iter()
                    .find(|a| &a.bundle_id == id)
                    .ok_or_else(|| BridgeError::new(Code::AppNotFound, format!("no app {id} on the phone")))?
                    .path,
            ),
            AppTarget::Pid(_) => None,
        };
        if !self.has_service::<RemoteServerClient<Box<dyn ReadWrite>>>() {
            return Err(self.missing_service("terminate app").await);
        }
        let work = async {
            let server = RemoteServerClient::connect_rsd(&mut self.adapter, &mut self.rsd)
                .await
                .map_err(|e| from_idevice(e, Phase::Operation, "terminate app"))?;
            let mut procs = Instruments { server };
            terminate(&mut procs, &target, bundle_path.as_deref()).await
        };
        answer_within(work, "terminate app").await?
    }
}

/// A running process as the instruments service lists it.
#[derive(Debug, Clone, PartialEq)]
struct Process {
    pid: u32,
    executable: String,
    is_app: bool,
}

/// Two ways the instruments service is asked to end a process.
#[derive(Debug, Clone, Copy, PartialEq)]
enum Kill {
    /// `killPid:` with the pid as an archived number.
    KillPid,
    /// `sendSignal:toPid:` with SIGKILL.
    Signal,
}

/// The phone's processes, for `terminate`; the instruments service on a phone, a stand-in in tests.
#[allow(async_fn_in_trait)]
trait Processes {
    async fn list(&mut self) -> Result<Vec<Process>, BridgeError>;
    async fn kill(&mut self, pid: u32, how: Kill) -> Result<(), BridgeError>;
    async fn pause(&mut self, d: Duration);
}

/// How long a killed process gets to disappear from the list before the next try.
const KILL_SETTLE: Duration = Duration::from_millis(400);
const KILL_CHECKS: u32 = 4;

/// Ends the app process `target` names (by pid, or the main executable of the bundle at `bundle_path`), and
/// says it ended only once the phone no longer lists it.
async fn terminate<P: Processes>(
    p: &mut P,
    target: &AppTarget,
    bundle_path: Option<&str>,
) -> Result<Value, BridgeError> {
    let find = |list: &[Process]| {
        list.iter()
            .filter(|p| p.is_app)
            .find(|p| match (target, bundle_path) {
                (AppTarget::Pid(pid), _) => p.pid == *pid,
                (_, Some(path)) => main_executable_of(&p.executable, path),
                _ => false,
            })
            .map(|p| p.pid)
    };
    let Some(pid) = find(&p.list().await?) else {
        return Ok(json!({"terminated": false}));
    };
    for how in [Kill::KillPid, Kill::Signal] {
        p.kill(pid, how).await?;
        for _ in 0..KILL_CHECKS {
            p.pause(KILL_SETTLE).await;
            if !p.list().await?.iter().any(|x| x.pid == pid) {
                let how = match how {
                    Kill::KillPid => "kill",
                    Kill::Signal => "signal",
                };
                return Ok(json!({"terminated": true, "pid": pid, "how": how}));
            }
        }
    }
    Err(BridgeError::new(
        Code::Unsupported,
        format!(
            "terminate app: the phone accepted the request but process {pid} is still running; ending apps doesn't work on this phone"
        ),
    ))
}

/// Whether `executable` is the bundle's own executable, directly inside it (not an extension nested deeper).
fn main_executable_of(executable: &str, bundle_path: &str) -> bool {
    if bundle_path.is_empty() || !runs_from(executable, bundle_path) {
        return false;
    }
    let exe = executable.strip_prefix("file://").unwrap_or(executable);
    let exe = percent_decode(exe);
    let parent = exe.trim_end_matches('/').rsplit_once('/').map_or("", |(dir, _)| dir);
    let canon = |p: &str| {
        let p = p.trim_end_matches('/');
        p.strip_prefix("/private").unwrap_or(p).to_owned()
    };
    canon(parent) == canon(bundle_path)
}

struct Instruments {
    server: RemoteServerClient<Box<dyn ReadWrite>>,
}

impl Processes for Instruments {
    async fn list(&mut self) -> Result<Vec<Process>, BridgeError> {
        let fail = |e| from_idevice(e, Phase::Operation, "list processes");
        let list = DeviceInfoClient::new(&mut self.server)
            .await
            .map_err(fail)?
            .running_processes()
            .await
            .map_err(fail)?;
        Ok(list
            .into_iter()
            .map(|p| Process {
                pid: p.pid,
                executable: p.real_app_name,
                is_app: p.is_application,
            })
            .collect())
    }

    async fn kill(&mut self, pid: u32, how: Kill) -> Result<(), BridgeError> {
        let fail = |e| from_idevice(e, Phase::Operation, "terminate app");
        let mut channel = self
            .server
            .make_channel("com.apple.instruments.server.services.processcontrol")
            .await
            .map_err(fail)?;
        let (selector, args) = match how {
            Kill::KillPid => ("killPid:", vec![AuxValue::archived_value(i64::from(pid))]),
            Kill::Signal => (
                "sendSignal:toPid:",
                vec![AuxValue::archived_value(9i64), AuxValue::archived_value(i64::from(pid))],
            ),
        };
        channel
            .call_method(Some(plist::Value::String(selector.into())), Some(args), true)
            .await
            .map_err(fail)?;
        // The answer, if the phone sends one, says nothing reliable; the process list does.
        let _ = tokio::time::timeout(Duration::from_secs(2), channel.read_message()).await;
        Ok(())
    }

    async fn pause(&mut self, d: Duration) {
        tokio::time::sleep(d).await;
    }
}

/// The first iOS whose CoreDevice takes remote input (the screen stream and HID): older ones answer error 9021.
const FIRST_IOS_WITH_REMOTE_INPUT: u32 = 27;

fn input_supported(ios_major: u32) -> bool {
    ios_major >= FIRST_IOS_WITH_REMOTE_INPUT
}

fn input_needs_newer_ios(ios_major: u32) -> BridgeError {
    BridgeError::new(
        Code::Unsupported,
        format!(
            "this iPhone runs iOS {ios_major}, which doesn't support remote input; it needs iOS {FIRST_IOS_WITH_REMOTE_INPUT} or later"
        ),
    )
}

/// "18.7.3" → 18.
fn major_version(v: &str) -> Option<u32> {
    v.split('.').next()?.trim().parse().ok()
}

/// Runs `stop` no sooner than `INPUT_LINGER` after `last_input`.
async fn stop_after_linger(last_input: tokio::time::Instant, stop: impl std::future::Future<Output = ()>) {
    tokio::time::sleep_until(last_input + INPUT_LINGER).await;
    stop.await;
}

/// How long the app operations wait for the phone before saying it didn't answer, instead of running into the
/// request's deadline and ending the session.
const APP_ANSWER: Duration = Duration::from_secs(20);

async fn answer_within<T>(work: impl std::future::Future<Output = T>, what: &str) -> Result<T, BridgeError> {
    tokio::time::timeout(APP_ANSWER, work).await.map_err(|_| {
        BridgeError::new(
            Code::ServiceUnavailable,
            format!("{what}: the phone did not answer within {} s", APP_ANSWER.as_secs()),
        )
    })
}

const BROWSE_ATTRIBUTES: &[&str] = &[
    "CFBundleIdentifier",
    "CFBundleDisplayName",
    "CFBundleName",
    "CFBundleShortVersionString",
    "ApplicationType",
    "Path",
];

#[derive(Debug, Clone, PartialEq)]
struct InstalledApp {
    bundle_id: String,
    name: String,
    version: Option<String>,
    /// Apple's own (`ApplicationType` System), not one the user installed.
    system: bool,
    path: String,
}

/// The installation service's `Browse` answer, entries without a bundle ID skipped.
fn parse_browse(values: Vec<plist::Value>) -> Vec<InstalledApp> {
    let mut apps: Vec<InstalledApp> = values
        .iter()
        .filter_map(plist::Value::as_dictionary)
        .filter_map(|d| {
            let text = |k: &str| d.get(k).and_then(plist::Value::as_string).map(str::to_owned);
            let bundle_id = text("CFBundleIdentifier")?;
            Some(InstalledApp {
                name: text("CFBundleDisplayName")
                    .or_else(|| text("CFBundleName"))
                    .unwrap_or_else(|| bundle_id.clone()),
                version: text("CFBundleShortVersionString"),
                system: text("ApplicationType").is_some_and(|t| t != "User"),
                path: text("Path").unwrap_or_default(),
                bundle_id,
            })
        })
        .collect();
    apps.sort_by(|a, b| a.bundle_id.cmp(&b.bundle_id));
    apps
}

/// Whether an executable (a `file://` URL, percent-encoded) lies inside an app bundle (a plain path). Either
/// may carry the `/private` prefix of `/var` on iOS.
fn runs_from(executable_url: &str, bundle_path: &str) -> bool {
    fn canonical(path: &str) -> String {
        let path = path.trim_end_matches('/');
        let path = path.strip_prefix("/private").unwrap_or(path);
        format!("{path}/")
    }
    let path = executable_url.strip_prefix("file://").unwrap_or(executable_url);
    canonical(&percent_decode(path)).starts_with(&canonical(bundle_path))
}

fn percent_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let hex = |c: u8| (c as char).to_digit(16);
        if b[i] == b'%'
            && i + 2 < b.len()
            && let (Some(h), Some(l)) = (hex(b[i + 1]), hex(b[i + 2]))
        {
            out.push((h * 16 + l) as u8);
            i += 3;
            continue;
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

impl Device {
    /// `mount_ddi` (ddi.rs), then the service directory read again so the developer services show up in this
    /// session: after this call mounted the image, and when it found the image mounted (by Xcode, or by another
    /// session) but this session doesn't list the services yet. If they still aren't listed the mount stands and is
    /// reported; the session asks to be started afresh.
    async fn mount_ddi(&mut self, dir: Option<String>, force: bool) -> Result<Value, BridgeError> {
        let dir = crate::ddi::resolve_dir(dir.as_deref(), std::env::var_os(crate::ddi::DIR_ENV));
        let mut result = crate::ddi::mount(self, &dir, force).await?;
        if result["already"] == true && self.lists_developer_services() {
            return Ok(result);
        }
        let listed = self.read_services_again().await && self.lists_developer_services();
        if !listed {
            self.stale = true;
        }
        result["services_refreshed"] = json!(listed);
        Ok(result)
    }

    /// Whether this session's service directory lists the developer services the disk image brings: the ones
    /// `device_info` reads `ddi_mounted` from.
    fn lists_developer_services(&self) -> bool {
        self.has_service::<ScreenCaptureServiceClient<Box<dyn ReadWrite>>>()
            || (self.has_service::<UniversalHidServiceClient<Box<dyn ReadWrite>>>()
                && self.has_service::<IndigoHidClient<Box<dyn ReadWrite>>>()
                && self.has_service::<DisplayServiceClient<Box<dyn ReadWrite>>>())
    }

    /// Reads the service directory again, for services that came (or went) with a disk image since this session
    /// read it; the clients opened from the old one are dropped. False, keeping the old one, when it can't be read.
    async fn read_services_again(&mut self) -> bool {
        self.services_checked = tokio::time::Instant::now();
        let read = async {
            let stream = self.adapter.connect(self.rsd_port).await.ok()?;
            RsdHandshake::new(stream).await.ok()
        };
        let Ok(Some(rsd)) = tokio::time::timeout(SERVICES_REREAD_TIMEOUT, read).await else {
            return false;
        };
        self.rsd = rsd;
        self.springboard = None;
        self.apps = None;
        true
    }

    async fn mounter(&mut self) -> Result<ImageMounter, BridgeError> {
        ImageMounter::connect_rsd(&mut self.adapter, &mut self.rsd)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "image mounter"))
    }

    async fn cryptexd(&mut self) -> Result<Cryptexd, BridgeError> {
        Cryptexd::connect_rsd(&mut self.adapter, &mut self.rsd)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "cryptexd"))
    }
}

/// A signing request that failed on the way or was refused: Apple's signing server gave no ticket.
fn tss_error(e: IdeviceError) -> BridgeError {
    match e {
        IdeviceError::Socket(_) | IdeviceError::Http(_) | IdeviceError::UnexpectedResponse(_) => BridgeError::new(
            Code::TssFailed,
            format!("Apple's signing server (gs.apple.com) gave no ticket: {e}"),
        ),
        other => from_idevice(other, Phase::Operation, "personalization request"),
    }
}

/// A mount error: what the phone said, kept apart from a dropped link or a locked phone.
fn mount_error(e: IdeviceError, what: &str) -> BridgeError {
    let mapped = from_idevice(e, Phase::Operation, what);
    match mapped.code {
        Code::Disconnected | Code::DeveloperModeOff | Code::Locked | Code::Timeout => mapped,
        _ => BridgeError::new(Code::MountFailed, mapped.message),
    }
}

impl crate::ddi::Mounter for Device {
    async fn mounted(&mut self) -> Result<bool, BridgeError> {
        // The cryptex (how the image goes on from iOS 27, Xcode's included) is not among the image mounter's images.
        if self.takes_cryptex().await?
            && cryptexd_within(
                "look up installed cryptexes",
                cryptexd::installed_ddi(&mut self.adapter, &mut self.rsd),
            )
            .await?
            .is_some()
        {
            return Ok(true);
        }
        match self.mounter().await?.lookup_image(crate::ddi::IMAGE_TYPE).await {
            Ok(_) => Ok(true),
            Err(IdeviceError::NotFound) => Ok(false),
            Err(e) => Err(from_idevice(e, Phase::Operation, "look up mounted images")),
        }
    }

    async fn developer_mode(&mut self) -> Result<Option<bool>, BridgeError> {
        match self.mounter().await?.query_developer_mode_status().await {
            Ok(on) => Ok(Some(on)),
            Err(_) => Ok(self.developer_mode().await),
        }
    }

    async fn takes_cryptex(&mut self) -> Result<bool, BridgeError> {
        Ok(self.has_service::<Cryptexd>()
            && self
                .ios_major()
                .await
                .is_some_and(|m| m >= crate::ddi::CRYPTEX_FROM_IOS))
    }

    async fn locked(&mut self) -> Result<Option<bool>, BridgeError> {
        // What CoreDevice checks before it mounts the image: true while the phone wants its passcode.
        let [v] = self.lockdown_values([("PasswordProtected", None)]).await;
        Ok(v.and_then(|v| v.as_boolean()))
    }

    async fn cryptex_ticket(&mut self, identity: &plist::Dictionary) -> Result<Vec<u8>, BridgeError> {
        let domain = identity
            .get("Cryptex1,NonceDomain")
            .and_then(plist::Value::as_unsigned_integer)
            .ok_or_else(|| BridgeError::new(Code::DdiMissing, "the cryptex build has no Cryptex1,NonceDomain"))?;
        let (adapter, rsd) = (&mut self.adapter, &mut self.rsd);
        let chip = cryptexd_within("personalization identifiers", async {
            Cryptexd::connect_rsd(&mut *adapter, &mut *rsd)
                .await?
                .read_personalization_identifiers()
                .await
        })
        .await?;
        let nonce = cryptexd_within("cryptex nonce", async {
            Cryptexd::connect_rsd(&mut *adapter, &mut *rsd)
                .await?
                .cryptex_nonce(domain)
                .await
        })
        .await?;
        let mut request = TSSRequest::new();
        request
            .add_cryptex1_tags(identity, &chip, &nonce)
            .map_err(|e| BridgeError::internal(format!("the cryptex signing request: {e}")))?;
        let answer = request.send().await.map_err(tss_error)?;
        // The ticket comes under its own key from current servers, under the AP one from older ones.
        answer
            .as_dictionary()
            .and_then(|d| d.get("Cryptex1,Ticket").or_else(|| d.get("ApImg4Ticket")))
            .and_then(plist::Value::as_data)
            .map(<[u8]>::to_vec)
            .ok_or_else(|| {
                BridgeError::new(
                    Code::TssFailed,
                    "Apple's signing server (gs.apple.com) answered without a ticket for the cryptex",
                )
            })
    }

    async fn install_cryptex(&mut self, image: crate::ddi::CryptexImage, ticket: Vec<u8>) -> Result<(), BridgeError> {
        let assets = Cryptex1Assets::from_parts(
            image.image,
            image.trust_cache,
            image.info,
            image.volume_hash,
            image.identity,
        );
        let properties = assets
            .cryptex1_properties()
            .map_err(|e| BridgeError::new(Code::DdiMissing, format!("the cryptex build: {e}")))?;
        let request = CryptexInstallRequest::new(
            assets.image,
            assets.trustcache,
            ticket,
            assets.info,
            assets.volumehash,
            properties,
        );
        self.cryptexd()
            .await?
            .install(request)
            .await
            .map_err(|e| mount_error(e, "install the disk image"))
    }

    async fn identifiers(&mut self) -> Result<(u64, u64), BridgeError> {
        let ids = self
            .mounter()
            .await?
            .query_personalization_identifiers(None)
            .await
            .map_err(|e| from_idevice(e, Phase::Operation, "personalization identifiers"))?;
        let get = |k: &str| ids.get(k).and_then(plist::Value::as_unsigned_integer);
        match (get("BoardId"), get("ChipID")) {
            (Some(board), Some(chip)) => Ok((board, chip)),
            _ => Err(BridgeError::internal("the phone did not say its board and chip")),
        }
    }

    async fn device_ticket(&mut self, image_hash: Vec<u8>) -> Result<Option<Vec<u8>>, BridgeError> {
        // No ticket for this image is an error from the phone, which also closes this connection.
        Ok(self
            .mounter()
            .await?
            .query_personalization_manifest("DeveloperDiskImage", image_hash)
            .await
            .ok())
    }

    async fn signed_ticket(&mut self, manifest: &plist::Dictionary) -> Result<Vec<u8>, BridgeError> {
        let [ecid] = self.lockdown_values([("UniqueChipID", None)]).await;
        let ecid = ecid
            .and_then(|v| v.as_unsigned_integer())
            .ok_or_else(|| BridgeError::internal("the phone did not give the chip ID the signing request needs"))?;
        let mut mounter = self.mounter().await?;
        mounter.get_manifest_from_tss(manifest, ecid).await.map_err(tss_error)
    }

    async fn upload_and_mount(
        &mut self,
        image: Vec<u8>,
        ticket: Vec<u8>,
        trust_cache: Vec<u8>,
    ) -> Result<(), BridgeError> {
        let mut mounter = self.mounter().await?;
        mounter
            .upload_image(crate::ddi::IMAGE_TYPE, &image, ticket.clone())
            .await
            .map_err(|e| mount_error(e, "upload the disk image"))?;
        mounter
            .mount_image(crate::ddi::IMAGE_TYPE, ticket, Some(trust_cache), None)
            .await
            .map_err(|e| mount_error(e, "mount the disk image"))
    }
}

impl Phone for Device {
    async fn run(&mut self, action: Action) -> Result<Value, BridgeError> {
        if self.stale {
            return Err(BridgeError::new(
                Code::Disconnected,
                "the phone's services changed with the mounted disk image: connect again",
            ));
        }
        let input = action.is_input();
        let result = match action {
            Action::DeviceInfo => self.device_info().await,
            Action::Screenshot(opts) => self.screenshot(opts).await,
            Action::AxTree { .. } => Err(BridgeError::new(
                Code::Unsupported,
                "the accessibility tree is not available over this connection yet; use screenshot",
            )),
            Action::Keys(k) => self.keys(k).await.map(|()| json!({})),
            Action::Button { button, hold_ms } => self.button(button, hold_ms).await.map(|()| json!({})),
            Action::LaunchApp {
                bundle_id,
                args,
                env,
                kill_existing,
            } => self.launch_app(bundle_id, args, env, kill_existing).await,
            Action::TerminateApp(t) => self.terminate_app(t).await,
            Action::ListApps { include_system } => self.list_apps(include_system).await,
            Action::MountDdi { ddi_dir, force } => self.mount_ddi(ddi_dir, force).await,
            gesture => self.gesture(gesture).await.map(|()| json!({})),
        };
        if input {
            if let Some(e) = result.as_ref().err().filter(|e| e.code != Code::BadRequest) {
                // The input services may be out of step after a failure: start them afresh next time. A
                // request refused before anything was sent leaves them as they are.
                self.hid = None;
                self.indigo = None;
                self.close_gate(e.link_lost()).await;
            } else {
                if let Some(g) = self.gate.as_mut() {
                    g.last_used = tokio::time::Instant::now();
                }
                if let Err(e) = self.confirm_input().await {
                    self.hid = None;
                    self.indigo = None;
                    self.close_gate(e.link_lost()).await;
                    return Err(e);
                }
            }
        }
        result
    }

    fn idle_at(&self) -> Option<tokio::time::Instant> {
        self.gate.as_ref().map(|g| g.last_used + GATE_IDLE)
    }

    async fn idle(&mut self) {
        self.hid = None;
        self.indigo = None;
        self.close_gate(false).await;
    }

    async fn close(&mut self) {
        self.idle().await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn resolves_literals_and_zones() {
        assert_eq!(
            socket_addr("192.0.2.7", 49152).await.unwrap(),
            "192.0.2.7:49152".parse().unwrap()
        );
        assert_eq!(
            socket_addr("[2001:db8::1]", 1).await.unwrap(),
            "[2001:db8::1]:1".parse().unwrap()
        );
        let SocketAddr::V6(v6) = socket_addr("fe80::1%7", 1).await.unwrap() else {
            panic!()
        };
        assert_eq!(v6.scope_id(), 7);
        assert_eq!(
            socket_addr("fe80::1%no-such-interface0", 1).await.unwrap_err().code,
            Code::Unreachable
        );
        assert_eq!(socket_addr("zz::1%1", 1).await.unwrap_err().code, Code::Unreachable);
    }

    /// A stand-in process list: `dies_on` says which kill (if any) actually ends the process.
    struct Procs {
        list: Vec<Process>,
        dies_on: Option<Kill>,
        kills: Vec<(u32, Kill)>,
    }

    impl Processes for Procs {
        async fn list(&mut self) -> Result<Vec<Process>, BridgeError> {
            Ok(self.list.clone())
        }
        async fn kill(&mut self, pid: u32, how: Kill) -> Result<(), BridgeError> {
            self.kills.push((pid, how));
            if self.dies_on == Some(how) {
                self.list.retain(|p| p.pid != pid);
            }
            Ok(())
        }
        async fn pause(&mut self, _: Duration) {}
    }

    const SETTINGS: &str = "/Applications/Preferences.app";

    fn procs(dies_on: Option<Kill>) -> Procs {
        let p = |pid, exe: &str, is_app| Process {
            pid,
            executable: exe.into(),
            is_app,
        };
        Procs {
            list: vec![
                p(10, "/Applications/Preferences.app/PlugIns/Widget.appex/Widget", true),
                p(12411, "/Applications/Preferences.app/Preferences", true),
                p(7, "/usr/libexec/somed", false),
            ],
            dies_on,
            kills: Vec::new(),
        }
    }

    #[tokio::test]
    async fn terminate_says_true_only_when_the_process_is_gone() {
        let by_bundle = AppTarget::BundleId("com.apple.Preferences".into());
        let mut p = procs(Some(Kill::KillPid));
        assert_eq!(
            terminate(&mut p, &by_bundle, Some(SETTINGS)).await.unwrap(),
            json!({"terminated": true, "pid": 12411, "how": "kill"})
        );
        assert_eq!(
            p.kills,
            [(12411, Kill::KillPid)],
            "the main executable, not its extension"
        );

        let mut p = procs(Some(Kill::Signal));
        assert_eq!(
            terminate(&mut p, &by_bundle, Some(SETTINGS)).await.unwrap(),
            json!({"terminated": true, "pid": 12411, "how": "signal"})
        );
        assert_eq!(p.kills, [(12411, Kill::KillPid), (12411, Kill::Signal)]);

        let mut p = procs(None);
        let err = terminate(&mut p, &by_bundle, Some(SETTINGS)).await.unwrap_err();
        assert_eq!(err.code, Code::Unsupported);
        assert!(err.message.contains("still running"));
    }

    #[tokio::test]
    async fn terminate_leaves_what_isnt_running_or_isnt_an_app() {
        let mut p = procs(Some(Kill::KillPid));
        let gone = terminate(
            &mut p,
            &AppTarget::BundleId("com.example.x".into()),
            Some("/var/containers/Bundle/Application/1/X.app"),
        )
        .await
        .unwrap();
        assert_eq!(gone, json!({"terminated": false}));
        assert_eq!(
            terminate(&mut p, &AppTarget::Pid(7), None).await.unwrap(),
            json!({"terminated": false})
        );
        assert_eq!(
            terminate(&mut p, &AppTarget::Pid(999), None).await.unwrap(),
            json!({"terminated": false})
        );
        assert!(p.kills.is_empty());
        assert_eq!(
            terminate(&mut p, &AppTarget::Pid(10), None).await.unwrap()["terminated"],
            true
        );
    }

    #[test]
    fn finds_the_main_executable() {
        assert!(main_executable_of(
            "/private/var/containers/Bundle/Application/1/My App.app/My App",
            "/var/containers/Bundle/Application/1/My App.app"
        ));
        assert!(!main_executable_of(
            "/Applications/Preferences.app/PlugIns/W.appex/W",
            SETTINGS
        ));
        assert!(!main_executable_of("/Applications/Preferences.app/Preferences", ""));
    }

    /// The stream (what lets the phone take input) stops only after the phone had time for the last input, even
    /// when the process is about to end right after answering.
    #[tokio::test(start_paused = true)]
    async fn the_stream_outlives_the_last_input() {
        let log = std::cell::RefCell::new(Vec::new());
        let start = tokio::time::Instant::now();
        let input_at = start + Duration::from_millis(200);
        tokio::time::sleep_until(input_at).await;
        log.borrow_mut().push(("input", start.elapsed()));
        stop_after_linger(input_at, async {
            log.borrow_mut().push(("stream stopped", start.elapsed()))
        })
        .await;
        let log = log.into_inner();
        assert_eq!(log[0].0, "input");
        assert_eq!(log[1].0, "stream stopped");
        assert!(log[1].1 - log[0].1 >= INPUT_LINGER, "{log:?}");

        // Input long ago: no wait.
        let before = tokio::time::Instant::now();
        stop_after_linger(before - Duration::from_secs(5), async {}).await;
        assert_eq!(tokio::time::Instant::now(), before);
    }

    #[test]
    fn input_needs_ios_27() {
        assert_eq!(major_version("18.7.3"), Some(18));
        assert_eq!(major_version("27.0"), Some(27));
        assert_eq!(major_version("x"), None);
        assert!(!input_supported(18));
        assert!(input_supported(27));
        let e = input_needs_newer_ios(18);
        assert_eq!(e.code, Code::Unsupported);
        assert!(e.message.contains("iOS 18") && e.message.contains("iOS 27"));
    }

    #[test]
    fn reads_the_installation_services_app_list() {
        let app = |id: Option<&str>, name: &str, t: &str| {
            let mut d = plist::Dictionary::new();
            if let Some(id) = id {
                d.insert("CFBundleIdentifier".into(), id.into());
            }
            d.insert("CFBundleName".into(), name.into());
            d.insert("ApplicationType".into(), t.into());
            d.insert(
                "Path".into(),
                format!("/private/var/containers/Bundle/Application/1/{name}.app").into(),
            );
            plist::Value::Dictionary(d)
        };
        let apps = parse_browse(vec![
            app(Some("com.example.b"), "B", "User"),
            app(None, "Nameless", "User"),
            app(Some("com.apple.Preferences"), "Settings", "System"),
            plist::Value::String("odd".into()),
        ]);
        assert_eq!(apps.len(), 2);
        assert_eq!(apps[0].bundle_id, "com.apple.Preferences");
        assert!(apps[0].system);
        assert!(!apps[1].system);
        assert_eq!(apps[1].name, "B");
        assert!(runs_from("/var/containers/Bundle/Application/1/B.app/B", &apps[1].path));
    }

    #[test]
    fn matches_a_process_to_its_app() {
        let app = "/private/var/containers/Bundle/Application/0000/My App.app";
        assert!(runs_from(
            "file:///var/containers/Bundle/Application/0000/My%20App.app/My%20App",
            app
        ));
        assert!(runs_from(
            "file:///private/var/containers/Bundle/Application/0000/My%20App.app/My%20App",
            "/var/containers/Bundle/Application/0000/My App.app/"
        ));
        assert!(!runs_from(
            "file:///var/containers/Bundle/Application/0000/My%20App.appendix/x",
            app
        ));
        assert_eq!(percent_decode("a%2Fb%zz%4"), "a/b%zz%4");
    }

    fn temp_record() -> (std::path::PathBuf, std::path::PathBuf) {
        let dir = std::env::temp_dir().join(format!("persona-iphone-bridge-device-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("pairing.record");
        Record::generate().write(&path).unwrap();
        (dir, path)
    }

    fn local(port: u16) -> Target {
        Target {
            udid: "U".into(),
            host: "127.0.0.1".into(),
            port,
        }
    }

    async fn connect_err(target: &Target, record: &Path) -> BridgeError {
        let record = match crate::record::Stored::read(record) {
            Ok(crate::record::Stored::Network(r)) => *r,
            Ok(_) => panic!("a network record"),
            Err(e) => return e,
        };
        match Device::connect(target, record).await {
            Ok(_) => panic!("connected to a stand-in"),
            Err(e) => e,
        }
    }

    #[tokio::test]
    async fn nothing_listening_is_unreachable() {
        let (dir, record) = temp_record();
        let port = {
            let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            l.local_addr().unwrap().port()
        };
        assert_eq!(connect_err(&local(port), &record).await.code, Code::Unreachable);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn a_peer_that_is_not_a_phone_fails_cleanly() {
        let (dir, record) = temp_record();
        // One that hangs up at once, and one that answers with garbage.
        for reply in [&b""[..], b"HTTP/1.1 400 Bad Request\r\n\r\n"] {
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            let port = listener.local_addr().unwrap().port();
            let server = tokio::spawn(async move {
                use tokio::io::AsyncWriteExt as _;
                let (mut s, _) = listener.accept().await.unwrap();
                let _ = s.write_all(reply).await;
            });
            let err = tokio::time::timeout(Duration::from_secs(10), connect_err(&local(port), &record))
                .await
                .expect("gives up instead of hanging");
            assert!(
                matches!(err.code, Code::Unreachable | Code::PairingRejected | Code::TunnelFailed),
                "{err}"
            );
            server.await.unwrap();
        }
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn a_missing_record_fails_before_dialing() {
        let e = connect_err(&local(9), Path::new("/nonexistent/pairing.record")).await;
        assert_eq!(e.code, Code::PairingMissing);
    }
}
