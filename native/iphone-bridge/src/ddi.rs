//! `mount_ddi`: mount the developer disk image the phone's developer services (screenshots, input, apps) need.
//! The image comes from Xcode's copy on this computer and goes on the phone the way Xcode puts it there:
//!
//! - **From iOS 27, as a cryptex** through the phone's `cryptexd`: the manifest's one cryptex build, the same for
//!   every phone, with a ticket Apple's signing server (TSS) makes for this phone. A phone newer than the
//!   per-device builds in Xcode's folder takes it all the same.
//! - **Before that, as a personalized image** through the phone's image mounter: the build for the phone's board
//!   and chip, with a ticket the phone may already hold, else one from Apple's signing server.
//!
//! Nothing is downloaded and nothing about the phone is written anywhere.
//!
//! The steps are written against `Mounter`, so they can be tested without a phone; `device.rs` implements it
//! over the phone's image mounter and cryptexd.

use std::ffi::OsString;
use std::path::{Component, Path, PathBuf};

use serde_json::{Value, json};
use sha2::{Digest, Sha384};

use crate::error::{BridgeError, Code};

/// Where Xcode keeps the developer disk image.
pub const DEFAULT_DIR: &str = "/Library/Developer/DeveloperDiskImages/iOS_DDI/Restore";
pub const DIR_ENV: &str = "PERSONA_IPHONE_DDI_DIR";
/// What the phone calls the personalized image.
pub const IMAGE_TYPE: &str = "Personalized";
/// From this iOS major version the phone takes the image as a cryptex, as Xcode installs it there (CoreDevice
/// switches at 27.0).
pub const CRYPTEX_FROM_IOS: u32 = 27;
/// How the cryptex build's variant ends ("iOS Customer Developer Disk Image Cryptex").
const CRYPTEX_VARIANT: &str = "Developer Disk Image Cryptex";

/// The directory to use: the request's, else `$PERSONA_IPHONE_DDI_DIR`, else Xcode's.
pub fn resolve_dir(arg: Option<&str>, env: Option<OsString>) -> PathBuf {
    match (arg, env) {
        (Some(a), _) => PathBuf::from(a),
        (None, Some(e)) if !e.is_empty() => PathBuf::from(e),
        _ => PathBuf::from(DEFAULT_DIR),
    }
}

fn missing(dir: &Path, what: impl std::fmt::Display) -> BridgeError {
    BridgeError::new(
        Code::DdiMissing,
        format!(
            "{what} in {}: this needs Xcode's developer disk image, or pass ddi_dir / set {DIR_ENV} to a folder \
             holding BuildManifest.plist and the images",
            dir.display()
        ),
    )
}

/// The build manifest, as read and as bytes (the signing request needs the dictionary).
pub fn read_manifest(dir: &Path) -> Result<plist::Dictionary, BridgeError> {
    let path = dir.join("BuildManifest.plist");
    let bytes = std::fs::read(&path).map_err(|e| missing(dir, format!("cannot read BuildManifest.plist ({e})")))?;
    let value: plist::Value =
        plist::from_bytes(&bytes).map_err(|_| missing(dir, "BuildManifest.plist is not a property list"))?;
    value
        .into_dictionary()
        .ok_or_else(|| missing(dir, "BuildManifest.plist is not a dictionary"))
}

/// "0x0C" or 12.
fn number(v: Option<&plist::Value>) -> Option<u64> {
    match v? {
        plist::Value::String(s) => {
            let s = s.trim();
            match s.strip_prefix("0x").or_else(|| s.strip_prefix("0X")) {
                Some(hex) => u64::from_str_radix(hex, 16).ok(),
                None => s.parse().ok(),
            }
        }
        plist::Value::Integer(i) => i.as_unsigned(),
        _ => None,
    }
}

fn identities(manifest: &plist::Dictionary) -> impl Iterator<Item = &plist::Dictionary> {
    manifest
        .get("BuildIdentities")
        .and_then(plist::Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or_default()
        .iter()
        .filter_map(plist::Value::as_dictionary)
}

/// The path (relative to the directory) a build identity gives for one of its manifest entries.
fn entry_path(identity: &plist::Dictionary, key: &str) -> Option<String> {
    identity
        .get("Manifest")
        .and_then(plist::Value::as_dictionary)
        .and_then(|m| m.get(key))
        .and_then(plist::Value::as_dictionary)
        .and_then(|e| e.get("Info"))
        .and_then(plist::Value::as_dictionary)
        .and_then(|i| i.get("Path"))
        .and_then(plist::Value::as_string)
        .map(str::to_owned)
}

/// The image and trust cache (paths relative to the directory) of the build identity for this phone.
pub fn pick(manifest: &plist::Dictionary, board: u64, chip: u64) -> Result<(String, String), BridgeError> {
    for id in identities(manifest) {
        if number(id.get("ApBoardID")) != Some(board) || number(id.get("ApChipID")) != Some(chip) {
            continue;
        }
        if let (Some(image), Some(trust)) = (entry_path(id, "PersonalizedDMG"), entry_path(id, "LoadableTrustCache")) {
            return Ok((image, trust));
        }
    }
    Err(BridgeError::new(
        Code::DdiUnsupportedDevice,
        format!(
            "the developer disk image has no build for this phone (board {board:#x}, chip {chip:#x}); a newer Xcode may have one"
        ),
    ))
}

/// The cryptex build: its identity, which the signing request is made from, and its four payloads by their paths
/// relative to the directory.
#[derive(Debug, Clone, PartialEq)]
pub struct CryptexBuild {
    pub identity: plist::Dictionary,
    /// `Cryptex1,GenericDmg`: the disk image.
    pub image: String,
    /// `Cryptex1,GenericTrustCache`.
    pub trust_cache: String,
    /// `Cryptex1,CryptexInfoPlist`: names and versions the cryptex.
    pub info: String,
    /// `Cryptex1,GenericVolume`: the volume's root hash.
    pub volume_hash: String,
}

/// What installing the cryptex takes: its build identity and its four payloads, read from the directory.
pub struct CryptexImage {
    pub identity: plist::Dictionary,
    pub image: Vec<u8>,
    pub trust_cache: Vec<u8>,
    pub info: Vec<u8>,
    pub volume_hash: Vec<u8>,
}

/// The cryptex build, when the directory has one (Xcode 27 and later).
pub fn pick_cryptex(manifest: &plist::Dictionary, dir: &Path) -> Result<Option<CryptexBuild>, BridgeError> {
    let Some(identity) = identities(manifest).find(|id| {
        id.get("Info")
            .and_then(plist::Value::as_dictionary)
            .and_then(|i| i.get("Variant"))
            .and_then(plist::Value::as_string)
            .is_some_and(|v| v.ends_with(CRYPTEX_VARIANT))
    }) else {
        return Ok(None);
    };
    let path = |key: &str| {
        entry_path(identity, key)
            .ok_or_else(|| missing(dir, format!("BuildManifest.plist's cryptex build names no {key}")))
    };
    Ok(Some(CryptexBuild {
        image: path("Cryptex1,GenericDmg")?,
        trust_cache: path("Cryptex1,GenericTrustCache")?,
        info: path("Cryptex1,CryptexInfoPlist")?,
        volume_hash: path("Cryptex1,GenericVolume")?,
        identity: identity.clone(),
    }))
}

impl CryptexBuild {
    fn read(&self, dir: &Path) -> Result<CryptexImage, BridgeError> {
        Ok(CryptexImage {
            identity: self.identity.clone(),
            image: read_inside(dir, &self.image)?,
            trust_cache: read_inside(dir, &self.trust_cache)?,
            info: read_inside(dir, &self.info)?,
            volume_hash: read_inside(dir, &self.volume_hash)?,
        })
    }
}

/// Reads a file the manifest names, which must stay inside the directory.
fn read_inside(dir: &Path, rel: &str) -> Result<Vec<u8>, BridgeError> {
    let p = Path::new(rel);
    if rel.is_empty() || p.components().any(|c| !matches!(c, Component::Normal(_))) {
        return Err(missing(dir, format!("BuildManifest.plist names an odd path {rel:?}")));
    }
    std::fs::read(dir.join(p)).map_err(|e| missing(dir, format!("cannot read {rel} ({e})")))
}

/// The phone side of mounting.
#[allow(async_fn_in_trait)]
pub trait Mounter {
    /// Whether the image is mounted now, either way: a personalized image, or the cryptex.
    async fn mounted(&mut self) -> Result<bool, BridgeError>;
    async fn developer_mode(&mut self) -> Result<Option<bool>, BridgeError>;
    /// Whether the phone takes the image as a cryptex: iOS 27 and later, offering cryptexd.
    async fn takes_cryptex(&mut self) -> Result<bool, BridgeError>;
    /// Whether the phone is locked now (it wants its passcode), `None` when it doesn't say.
    async fn locked(&mut self) -> Result<Option<bool>, BridgeError>;
    /// A ticket from Apple's signing server for the cryptex build's identity on this phone.
    async fn cryptex_ticket(&mut self, identity: &plist::Dictionary) -> Result<Vec<u8>, BridgeError>;
    async fn install_cryptex(&mut self, image: CryptexImage, ticket: Vec<u8>) -> Result<(), BridgeError>;
    /// The phone's board and chip IDs.
    async fn identifiers(&mut self) -> Result<(u64, u64), BridgeError>;
    /// A personalization ticket the phone already holds for the image with this SHA-384, if any.
    async fn device_ticket(&mut self, image_hash: Vec<u8>) -> Result<Option<Vec<u8>>, BridgeError>;
    /// A ticket from Apple's signing server.
    async fn signed_ticket(&mut self, manifest: &plist::Dictionary) -> Result<Vec<u8>, BridgeError>;
    async fn upload_and_mount(
        &mut self,
        image: Vec<u8>,
        ticket: Vec<u8>,
        trust_cache: Vec<u8>,
    ) -> Result<(), BridgeError>;
}

pub async fn mount<M: Mounter>(m: &mut M, dir: &Path, force: bool) -> Result<Value, BridgeError> {
    if !force && m.mounted().await? {
        return Ok(json!({"mounted": true, "already": true}));
    }
    if m.developer_mode().await? == Some(false) {
        return Err(BridgeError::new(
            Code::DeveloperModeOff,
            "Developer Mode is off on the phone: Settings › Privacy & Security › Developer Mode",
        ));
    }
    let manifest = read_manifest(dir)?;
    // As Xcode does: the cryptex from iOS 27. A directory without a cryptex build (an older Xcode) may still have
    // a per-device build for this phone.
    if m.takes_cryptex().await?
        && let Some(build) = pick_cryptex(&manifest, dir)?
    {
        return mount_cryptex(m, dir, &build).await;
    }
    let (board, chip) = m.identifiers().await?;
    let (image_name, trust_name) = pick(&manifest, board, chip)?;
    let image = read_inside(dir, &image_name)?;
    let trust_cache = read_inside(dir, &trust_name)?;
    let hash = Sha384::digest(&image).to_vec();
    let (ticket, source) = match m.device_ticket(hash).await? {
        Some(t) => (t, "phone"),
        None => (m.signed_ticket(&manifest).await?, "apple"),
    };
    m.upload_and_mount(image, ticket, trust_cache).await?;
    if !m.mounted().await? {
        return Err(BridgeError::new(
            Code::MountFailed,
            "the phone accepted the image but does not list it as mounted",
        ));
    }
    Ok(json!({"mounted": true, "already": false, "kind": "personalized", "image": image_name, "ticket": source}))
}

async fn mount_cryptex<M: Mounter>(m: &mut M, dir: &Path, build: &CryptexBuild) -> Result<Value, BridgeError> {
    let image = build.read(dir)?;
    // A locked phone never answers the install (seen on iOS 27.2.1: nothing for minutes), so ask first, as Xcode does.
    if m.locked().await? == Some(true) {
        return Err(BridgeError::new(
            Code::Locked,
            "the phone is locked: unlock it and keep it awake while the developer disk image is installed",
        ));
    }
    // The ticket first: nothing goes to the phone without one.
    let ticket = m.cryptex_ticket(&build.identity).await?;
    m.install_cryptex(image, ticket).await?;
    if !m.mounted().await? {
        return Err(BridgeError::new(
            Code::MountFailed,
            "the phone accepted the cryptex but does not list it as installed",
        ));
    }
    Ok(json!({"mounted": true, "already": false, "kind": "cryptex", "image": build.image, "ticket": "apple"}))
}

#[cfg(test)]
mod tests {
    use super::*;

    const BOARD: u64 = 0x0C;
    const CHIP: u64 = 0x8101;

    fn entry(path: &str) -> plist::Value {
        let mut info = plist::Dictionary::new();
        info.insert("Path".into(), path.into());
        let mut e = plist::Dictionary::new();
        e.insert("Info".into(), plist::Value::Dictionary(info));
        plist::Value::Dictionary(e)
    }

    fn identity(board: &str, chip: &str, image: &str) -> plist::Value {
        let mut m = plist::Dictionary::new();
        m.insert("PersonalizedDMG".into(), entry(image));
        m.insert(
            "LoadableTrustCache".into(),
            entry(&format!("Firmware/{image}.trustcache")),
        );
        let mut id = plist::Dictionary::new();
        id.insert("ApBoardID".into(), board.into());
        id.insert("ApChipID".into(), chip.into());
        id.insert("Manifest".into(), plist::Value::Dictionary(m));
        plist::Value::Dictionary(id)
    }

    /// The cryptex build, shaped like Xcode 27's: one identity for every phone, naming its four payloads.
    fn cryptex_identity(image: &str) -> plist::Value {
        let mut m = plist::Dictionary::new();
        m.insert("Cryptex1,GenericDmg".into(), entry(image));
        m.insert(
            "Cryptex1,GenericTrustCache".into(),
            entry(&format!("Firmware/{image}.trustcache")),
        );
        m.insert(
            "Cryptex1,CryptexInfoPlist".into(),
            entry(&format!("Firmware/{image}.cryptex_info")),
        );
        m.insert(
            "Cryptex1,GenericVolume".into(),
            entry(&format!("Firmware/{image}.root_hash")),
        );
        let mut info = plist::Dictionary::new();
        info.insert("Variant".into(), "iOS Customer Developer Disk Image Cryptex".into());
        let mut id = plist::Dictionary::new();
        id.insert("Cryptex1,ChipID".into(), "0xFF10".into());
        id.insert("Cryptex1,NonceDomain".into(), plist::Value::Integer(4u64.into()));
        id.insert("Info".into(), plist::Value::Dictionary(info));
        id.insert("Manifest".into(), plist::Value::Dictionary(m));
        plist::Value::Dictionary(id)
    }

    fn manifest(ids: Vec<plist::Value>) -> plist::Dictionary {
        let mut d = plist::Dictionary::new();
        d.insert("BuildIdentities".into(), plist::Value::Array(ids));
        d
    }

    fn write_manifest(dir: &Path, ids: Vec<plist::Value>) {
        plist::Value::Dictionary(manifest(ids))
            .to_file_xml(dir.join("BuildManifest.plist"))
            .unwrap();
    }

    /// A made-up DDI folder: per-device images for two chip families, and the cryptex every phone shares.
    fn ddi_dir() -> PathBuf {
        let dir = std::env::temp_dir().join(format!("persona-ddi-{}", uuid::Uuid::new_v4().simple()));
        std::fs::create_dir_all(dir.join("Firmware")).unwrap();
        write_manifest(
            &dir,
            vec![
                identity("0x0C", "0x8010", "a.dmg"),
                identity("0x0C", "0x8101", "b.dmg"),
                cryptex_identity("c.dmg"),
            ],
        );
        for f in ["a.dmg", "b.dmg", "c.dmg"] {
            std::fs::write(dir.join(f), format!("image {f}")).unwrap();
            std::fs::write(
                dir.join("Firmware").join(format!("{f}.trustcache")),
                format!("trust {f}"),
            )
            .unwrap();
        }
        let firmware = dir.join("Firmware");
        std::fs::write(firmware.join("c.dmg.cryptex_info"), "info c.dmg").unwrap();
        std::fs::write(firmware.join("c.dmg.root_hash"), "volume c.dmg").unwrap();
        dir
    }

    #[derive(Default)]
    struct Fake {
        mounted: bool,
        developer_mode: Option<bool>,
        /// iOS 27 or later: the phone takes the image as a cryptex.
        cryptex: bool,
        locked: Option<bool>,
        ids: Option<(u64, u64)>,
        phone_ticket: Option<Vec<u8>>,
        signing: Option<BridgeError>,
        cryptex_signing: Option<BridgeError>,
        refuse_mount: Option<BridgeError>,
        refuse_install: Option<BridgeError>,
        /// Mounting succeeds but the phone still lists nothing.
        silent: bool,
        log: Vec<String>,
    }

    impl Mounter for Fake {
        async fn mounted(&mut self) -> Result<bool, BridgeError> {
            self.log.push("lookup".into());
            Ok(self.mounted)
        }
        async fn developer_mode(&mut self) -> Result<Option<bool>, BridgeError> {
            Ok(self.developer_mode)
        }
        async fn takes_cryptex(&mut self) -> Result<bool, BridgeError> {
            Ok(self.cryptex)
        }
        async fn locked(&mut self) -> Result<Option<bool>, BridgeError> {
            Ok(self.locked)
        }
        async fn cryptex_ticket(&mut self, identity: &plist::Dictionary) -> Result<Vec<u8>, BridgeError> {
            assert!(identity.contains_key("Cryptex1,ChipID"), "the cryptex build's identity");
            self.log.push("cryptex ticket".into());
            match self.cryptex_signing.clone() {
                Some(e) => Err(e),
                None => Ok(b"cryptex-ticket".to_vec()),
            }
        }
        async fn install_cryptex(&mut self, image: CryptexImage, ticket: Vec<u8>) -> Result<(), BridgeError> {
            let text = |b: &[u8]| String::from_utf8_lossy(b).into_owned();
            self.log.push(format!(
                "install {} {} {} {} {}",
                text(&image.image),
                text(&ticket),
                text(&image.trust_cache),
                text(&image.info),
                text(&image.volume_hash)
            ));
            if let Some(e) = self.refuse_install.clone() {
                return Err(e);
            }
            self.mounted = !self.silent;
            Ok(())
        }
        async fn identifiers(&mut self) -> Result<(u64, u64), BridgeError> {
            self.ids
                .ok_or_else(|| BridgeError::internal("the phone did not say its board and chip"))
        }
        async fn device_ticket(&mut self, image_hash: Vec<u8>) -> Result<Option<Vec<u8>>, BridgeError> {
            assert_eq!(image_hash.len(), 48, "SHA-384");
            self.log.push("phone ticket".into());
            Ok(self.phone_ticket.clone())
        }
        async fn signed_ticket(&mut self, manifest: &plist::Dictionary) -> Result<Vec<u8>, BridgeError> {
            assert!(manifest.contains_key("BuildIdentities"));
            self.log.push("apple ticket".into());
            match self.signing.clone() {
                Some(e) => Err(e),
                None => Ok(b"ticket".to_vec()),
            }
        }
        async fn upload_and_mount(
            &mut self,
            image: Vec<u8>,
            ticket: Vec<u8>,
            trust_cache: Vec<u8>,
        ) -> Result<(), BridgeError> {
            self.log.push(format!(
                "mount {} {} {}",
                String::from_utf8_lossy(&image),
                String::from_utf8_lossy(&ticket),
                String::from_utf8_lossy(&trust_cache)
            ));
            if let Some(e) = self.refuse_mount.clone() {
                return Err(e);
            }
            self.mounted = !self.silent;
            Ok(())
        }
    }

    fn phone() -> Fake {
        Fake {
            developer_mode: Some(true),
            ids: Some((BOARD, CHIP)),
            ..Fake::default()
        }
    }

    /// An iOS 27 phone newer than every per-device build in the folder (no board and chip to ask for).
    fn ios27() -> Fake {
        Fake {
            developer_mode: Some(true),
            cryptex: true,
            ..Fake::default()
        }
    }

    #[test]
    fn finds_the_directory() {
        assert_eq!(resolve_dir(Some("/a"), Some("/b".into())), PathBuf::from("/a"));
        assert_eq!(resolve_dir(None, Some("/b".into())), PathBuf::from("/b"));
        assert_eq!(resolve_dir(None, Some("".into())), PathBuf::from(DEFAULT_DIR));
        assert_eq!(resolve_dir(None, None), PathBuf::from(DEFAULT_DIR));
    }

    #[test]
    fn picks_the_build_for_this_phone() {
        let m = manifest(vec![
            identity("0x0C", "0x8010", "a.dmg"),
            identity("0x0C", "0x8101", "b.dmg"),
        ]);
        assert_eq!(
            pick(&m, 0x0C, 0x8101).unwrap(),
            ("b.dmg".to_owned(), "Firmware/b.dmg.trustcache".to_owned())
        );
        assert_eq!(pick(&m, 0x0E, 0x8101).unwrap_err().code, Code::DdiUnsupportedDevice);
        assert_eq!(
            pick(&plist::Dictionary::new(), 1, 1).unwrap_err().code,
            Code::DdiUnsupportedDevice
        );
        assert_eq!(number(Some(&plist::Value::String("12".into()))), Some(12));
        assert_eq!(number(Some(&plist::Value::String("0xzz".into()))), None);
    }

    #[test]
    fn stays_inside_the_directory() {
        let dir = ddi_dir();
        assert!(read_inside(&dir, "a.dmg").is_ok());
        for odd in ["../a.dmg", "/etc/hosts", "", "Firmware/../../x"] {
            assert_eq!(read_inside(&dir, odd).unwrap_err().code, Code::DdiMissing, "{odd}");
        }
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn mounts_with_the_phones_own_ticket_first() {
        let dir = ddi_dir();
        let mut p = phone();
        p.phone_ticket = Some(b"kept".to_vec());
        let r = mount(&mut p, &dir, false).await.unwrap();
        assert_eq!(
            r,
            json!({"mounted": true, "already": false, "kind": "personalized", "image": "b.dmg", "ticket": "phone"})
        );
        assert_eq!(
            p.log,
            ["lookup", "phone ticket", "mount image b.dmg kept trust b.dmg", "lookup"]
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn asks_apple_when_the_phone_has_no_ticket() {
        let dir = ddi_dir();
        let mut p = phone();
        let r = mount(&mut p, &dir, false).await.unwrap();
        assert_eq!(r["ticket"], "apple");
        assert!(p.log.contains(&"mount image b.dmg ticket trust b.dmg".to_owned()));

        let mut p = phone();
        p.signing = Some(BridgeError::new(Code::TssFailed, "no answer"));
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::TssFailed);
        assert!(
            !p.log.iter().any(|l| l.starts_with("mount")),
            "nothing uploaded without a ticket"
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn is_idempotent_unless_forced() {
        let dir = ddi_dir();
        let mut p = phone();
        p.mounted = true;
        assert_eq!(
            mount(&mut p, &dir, false).await.unwrap(),
            json!({"mounted": true, "already": true})
        );
        assert_eq!(p.log, ["lookup"]);
        let r = mount(&mut p, &dir, true).await.unwrap();
        assert_eq!(r["already"], false);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn says_what_is_missing() {
        let mut p = phone();
        let err = mount(&mut p, Path::new("/nonexistent/persona-ddi"), false)
            .await
            .unwrap_err();
        assert_eq!(err.code, Code::DdiMissing);
        assert!(err.message.contains(DIR_ENV));

        let dir = ddi_dir();
        let mut p = phone();
        p.developer_mode = Some(false);
        assert_eq!(
            mount(&mut p, &dir, false).await.unwrap_err().code,
            Code::DeveloperModeOff
        );

        let mut p = phone();
        p.ids = Some((0x0E, 0x9999));
        assert_eq!(
            mount(&mut p, &dir, false).await.unwrap_err().code,
            Code::DdiUnsupportedDevice
        );

        let mut p = phone();
        p.ids = None;
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::Internal);

        let mut p = phone();
        p.refuse_mount = Some(BridgeError::new(Code::MountFailed, "refused"));
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::MountFailed);

        let mut p = phone();
        p.silent = true;
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::MountFailed);

        std::fs::remove_file(dir.join("b.dmg")).unwrap();
        let mut p = phone();
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::DdiMissing);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn picks_the_cryptex_build() {
        let dir = Path::new("/nonexistent/persona-ddi");
        let m = manifest(vec![identity("0x0C", "0x8101", "b.dmg"), cryptex_identity("c.dmg")]);
        let build = pick_cryptex(&m, dir).unwrap().unwrap();
        assert_eq!(
            [build.image, build.trust_cache, build.info, build.volume_hash],
            [
                "c.dmg",
                "Firmware/c.dmg.trustcache",
                "Firmware/c.dmg.cryptex_info",
                "Firmware/c.dmg.root_hash"
            ]
        );
        assert!(build.identity.contains_key("Cryptex1,ChipID"));

        // An older Xcode's folder has per-device builds only.
        let old = manifest(vec![identity("0x0C", "0x8101", "b.dmg")]);
        assert_eq!(pick_cryptex(&old, dir).unwrap(), None);

        // A cryptex build that doesn't name one of its payloads leaves nothing to install.
        let mut partial = cryptex_identity("c.dmg");
        partial
            .as_dictionary_mut()
            .and_then(|id| id.get_mut("Manifest"))
            .and_then(plist::Value::as_dictionary_mut)
            .unwrap()
            .remove("Cryptex1,GenericVolume");
        assert_eq!(
            pick_cryptex(&manifest(vec![partial]), dir).unwrap_err().code,
            Code::DdiMissing
        );
    }

    #[tokio::test]
    async fn installs_the_cryptex_from_ios_27() {
        // The phone's board and chip are in no per-device build (it can't even say them here): the cryptex still fits.
        let dir = ddi_dir();
        let mut p = ios27();
        let r = mount(&mut p, &dir, false).await.unwrap();
        assert_eq!(
            r,
            json!({"mounted": true, "already": false, "kind": "cryptex", "image": "c.dmg", "ticket": "apple"})
        );
        assert_eq!(
            p.log,
            [
                "lookup",
                "cryptex ticket",
                "install image c.dmg cryptex-ticket trust c.dmg info c.dmg volume c.dmg",
                "lookup"
            ]
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn a_cryptex_already_installed_counts_as_mounted() {
        // Xcode installed it, or another session did: nothing to do.
        let dir = ddi_dir();
        let mut p = ios27();
        p.mounted = true;
        assert_eq!(
            mount(&mut p, &dir, false).await.unwrap(),
            json!({"mounted": true, "already": true})
        );
        assert_eq!(p.log, ["lookup"]);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn the_cryptex_goes_to_the_phone_only_with_its_ticket() {
        let dir = ddi_dir();
        let mut p = ios27();
        p.cryptex_signing = Some(BridgeError::new(Code::TssFailed, "no answer"));
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::TssFailed);
        assert!(
            !p.log.iter().any(|l| l.starts_with("install")),
            "nothing installed without a ticket"
        );

        let mut p = ios27();
        p.refuse_install = Some(BridgeError::new(Code::MountFailed, "refused"));
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::MountFailed);

        let mut p = ios27();
        p.silent = true;
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::MountFailed);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn a_locked_phone_is_asked_to_unlock_before_anything_is_sent() {
        let dir = ddi_dir();
        let mut p = ios27();
        p.locked = Some(true);
        let err = mount(&mut p, &dir, false).await.unwrap_err();
        assert_eq!(err.code, Code::Locked);
        assert!(err.message.contains("unlock"), "{}", err.message);
        assert_eq!(p.log, ["lookup"], "neither a signing request nor an install");

        // A phone that doesn't say is tried.
        let mut p = ios27();
        p.locked = None;
        assert_eq!(mount(&mut p, &dir, false).await.unwrap()["kind"], "cryptex");
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn without_a_cryptex_build_the_per_device_build_is_tried() {
        // An older Xcode's folder on an iOS 27 phone: the phone's own build if the folder has one.
        let dir = ddi_dir();
        write_manifest(
            &dir,
            vec![identity("0x0C", "0x8010", "a.dmg"), identity("0x0C", "0x8101", "b.dmg")],
        );
        let mut p = ios27();
        p.ids = Some((BOARD, CHIP));
        let r = mount(&mut p, &dir, false).await.unwrap();
        assert_eq!((&r["kind"], &r["image"]), (&json!("personalized"), &json!("b.dmg")));
        assert!(!p.log.contains(&"cryptex ticket".to_owned()));

        let mut p = ios27();
        p.ids = Some((0x0E, 0x8160));
        assert_eq!(
            mount(&mut p, &dir, false).await.unwrap_err().code,
            Code::DdiUnsupportedDevice
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[tokio::test]
    async fn the_cryptex_payloads_stay_inside_the_directory() {
        let dir = ddi_dir();
        let mut odd = cryptex_identity("c.dmg");
        odd.as_dictionary_mut()
            .and_then(|id| id.get_mut("Manifest"))
            .and_then(plist::Value::as_dictionary_mut)
            .unwrap()
            .insert("Cryptex1,GenericVolume".into(), entry("../c.dmg.root_hash"));
        write_manifest(&dir, vec![odd]);
        let mut p = ios27();
        assert_eq!(mount(&mut p, &dir, false).await.unwrap_err().code, Code::DdiMissing);
        assert_eq!(p.log, ["lookup"], "neither a signing request nor an install");
        std::fs::remove_dir_all(dir).unwrap();
    }
}
