//! A local transport adapter for one captured HTTPS authority, not a general proxy.
use base64::{Engine, engine::general_purpose::STANDARD};
use std::net::IpAddr;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

pub(crate) const MAX_HANDSHAKE: usize = 8192;
pub(crate) type Result<T> = std::result::Result<T, &'static str>;

pub(crate) fn valid_host(value: &str) -> bool {
    if value.is_empty() || value.len() > 253 { return false; }
    if value.parse::<IpAddr>().is_ok() { return true; }
    value.is_ascii() && value.split('.').all(|label| !label.is_empty() && label.len() <= 63 &&
        label.bytes().all(|c| c.is_ascii_alphanumeric() || c == b'-') &&
        !label.starts_with('-') && !label.ends_with('-'))
}

pub(crate) fn same_host(left: &str, right: &str) -> bool {
    match (left.parse::<IpAddr>(), right.parse::<IpAddr>()) {
        (Ok(left), Ok(right)) => left == right,
        _ => left.eq_ignore_ascii_case(right),
    }
}

fn authority(value: &str) -> Result<(&str, u16)> {
    let (host, port) = if let Some(rest) = value.strip_prefix('[') {
        let (host, port) = rest.split_once("]:").ok_or("ssh_proxy_target")?;
        if !matches!(host.parse::<IpAddr>(), Ok(IpAddr::V6(_))) { return Err("ssh_proxy_target"); }
        (host, port)
    } else {
        let (host, port) = value.rsplit_once(':').ok_or("ssh_proxy_target")?;
        if host.contains(':') { return Err("ssh_proxy_target"); }
        (host, port)
    };
    let port = port.parse::<u16>().map_err(|_| "ssh_proxy_target")?;
    if port == 0 || !valid_host(host) { return Err("ssh_proxy_target"); }
    Ok((host, port))
}

// No early-exit comparison of the ephemeral credential's bytes.
fn equal_secret(left: &[u8], right: &[u8]) -> bool {
    let mut difference = left.len() ^ right.len();
    for index in 0..left.len().max(right.len()) {
        difference |= usize::from(left.get(index).copied().unwrap_or(0) ^ right.get(index).copied().unwrap_or(0));
    }
    difference == 0
}

#[derive(Clone)]
pub(crate) struct Credentials { pub username: String, pub password: String }

#[derive(Clone, Copy)]
pub(crate) enum Protocol { Http, Socks }

pub(crate) fn verify_http(bytes: &[u8], host: &str, port: u16, credentials: &Credentials) -> Result<()> {
    if bytes.len() > MAX_HANDSHAKE || !bytes.ends_with(b"\r\n\r\n") { return Err("ssh_proxy_handshake"); }
    let text = std::str::from_utf8(bytes).map_err(|_| "ssh_proxy_handshake")?;
    if text.bytes().any(|c| c == 0 || c > 126 || c < 32 && c != b'\r' && c != b'\n' && c != b'\t') { return Err("ssh_proxy_handshake"); }
    let mut lines = text[..text.len() - 4].split("\r\n");
    let parts: Vec<&str> = lines.next().ok_or("ssh_proxy_handshake")?.split(' ').collect();
    if parts.len() != 3 || parts[0] != "CONNECT" || !["HTTP/1.0", "HTTP/1.1"].contains(&parts[2]) { return Err("ssh_proxy_handshake"); }
    let (requested_host, requested_port) = authority(parts[1])?;
    if !same_host(requested_host, host) || requested_port != port { return Err("ssh_proxy_target"); }
    let mut auth = None;
    for line in lines {
        if line.starts_with(' ') || line.starts_with('\t') { return Err("ssh_proxy_handshake"); }
        let (name, value) = line.split_once(':').ok_or("ssh_proxy_handshake")?;
        if name.eq_ignore_ascii_case("proxy-authorization") {
            if auth.is_some() { return Err("ssh_proxy_auth"); }
            auth = Some(value.trim());
        } else if name.eq_ignore_ascii_case("content-length") || name.eq_ignore_ascii_case("transfer-encoding") {
            return Err("ssh_proxy_handshake");
        } else if name.eq_ignore_ascii_case("host") {
            let (header_host, header_port) = authority(value.trim())?;
            if !same_host(header_host, host) || header_port != port { return Err("ssh_proxy_target"); }
        }
    }
    let supplied = auth.and_then(|value| value.split_once(' ')).filter(|(scheme, _)| scheme.eq_ignore_ascii_case("basic"))
        .and_then(|(_, value)| STANDARD.decode(value).ok()).ok_or("ssh_proxy_auth")?;
    let expected = format!("{}:{}", credentials.username, credentials.password);
    if !equal_secret(&supplied, expected.as_bytes()) { return Err("ssh_proxy_auth"); }
    Ok(())
}

pub(crate) async fn handshake<S: AsyncRead + AsyncWrite + Unpin>(stream: &mut S, host: &str, port: u16,
    resolved_hub_ips: &[IpAddr], credentials: &Credentials) -> Result<Protocol> {
    let first = stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?;
    if first != 5 {
        let mut bytes = vec![first];
        while !bytes.ends_with(b"\r\n\r\n") {
            if bytes.len() >= MAX_HANDSHAKE { return Err("ssh_proxy_handshake"); }
            bytes.push(stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?);
        }
        verify_http(&bytes, host, port, credentials)?;
        return Ok(Protocol::Http);
    }
    let count = stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?;
    if count == 0 || count > 8 { return Err("ssh_proxy_handshake"); }
    let mut methods = vec![0; usize::from(count)];
    stream.read_exact(&mut methods).await.map_err(|_| "ssh_proxy_handshake")?;
    if !methods.contains(&2) { stream.write_all(&[5, 255]).await.ok(); return Err("ssh_proxy_auth"); }
    stream.write_all(&[5, 2]).await.map_err(|_| "ssh_proxy_handshake")?;
    if stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")? != 1 { return Err("ssh_proxy_auth"); }
    let length = stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?;
    let mut user = vec![0; usize::from(length)]; stream.read_exact(&mut user).await.map_err(|_| "ssh_proxy_handshake")?;
    let length = stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?;
    let mut password = vec![0; usize::from(length)]; stream.read_exact(&mut password).await.map_err(|_| "ssh_proxy_handshake")?;
    if !equal_secret(&user, credentials.username.as_bytes()) || !equal_secret(&password, credentials.password.as_bytes()) {
        stream.write_all(&[1, 1]).await.ok(); return Err("ssh_proxy_auth");
    }
    stream.write_all(&[1, 0]).await.map_err(|_| "ssh_proxy_handshake")?;
    let mut header = [0; 4]; stream.read_exact(&mut header).await.map_err(|_| "ssh_proxy_handshake")?;
    if header[0] != 5 || header[1] != 1 || header[2] != 0 { return Err("ssh_proxy_target"); }
    let requested_host = match header[3] {
        1 => { let mut value = [0; 4]; stream.read_exact(&mut value).await.map_err(|_| "ssh_proxy_handshake")?; std::net::Ipv4Addr::from(value).to_string() }
        4 => { let mut value = [0; 16]; stream.read_exact(&mut value).await.map_err(|_| "ssh_proxy_handshake")?; std::net::Ipv6Addr::from(value).to_string() }
        3 => { let length = stream.read_u8().await.map_err(|_| "ssh_proxy_handshake")?;
            let mut value = vec![0; usize::from(length)]; stream.read_exact(&mut value).await.map_err(|_| "ssh_proxy_handshake")?;
            String::from_utf8(value).map_err(|_| "ssh_proxy_target")? }
        _ => return Err("ssh_proxy_target"),
    };
    let requested_port = stream.read_u16().await.map_err(|_| "ssh_proxy_handshake")?;
    let same_logical_host = same_host(&requested_host, host);
    let captured_ip = requested_host.parse::<IpAddr>().is_ok_and(|address| resolved_hub_ips.contains(&address));
    if (!same_logical_host && !captured_ip) || requested_port != port { return Err("ssh_proxy_target"); }
    Ok(Protocol::Socks)
}

pub(crate) async fn accepted<S: AsyncWrite + Unpin>(stream: &mut S, protocol: Protocol) -> Result<()> {
    let reply: &[u8] = match protocol { Protocol::Http => b"HTTP/1.1 200 Connection Established\r\n\r\n", Protocol::Socks => &[5, 0, 0, 1, 127, 0, 0, 1, 0, 0] };
    stream.write_all(reply).await.map_err(|_| "ssh_proxy_handshake")
}

#[cfg(test)]
mod tests {
    use super::*;
    fn credentials() -> Credentials { Credentials { username: "local".into(), password: "secret".into() } }
    fn request(target: &str) -> Vec<u8> { format!("CONNECT {target} HTTP/1.1\r\nHost: {target}\r\nProxy-Authorization: Basic {}\r\n\r\n", STANDARD.encode("local:secret")).into_bytes() }
    fn socks_request(host: &[u8], address_type: u8, port: u16) -> Vec<u8> {
        let mut request = vec![5, 1, 2, 1, 5]; request.extend(b"local"); request.push(6); request.extend(b"secret");
        request.extend([5, 1, 0, address_type]);
        if address_type == 3 { request.push(host.len() as u8); }
        request.extend(host); request.extend(port.to_be_bytes()); request
    }
    #[test] fn connect_admits_only_exact_authority_and_credentials() {
        assert!(verify_http(&request("hub.test:443"), "hub.test", 443, &credentials()).is_ok());
        for target in ["other.test:443", "hub.test:80", "hub.test:443/path", "user@hub.test:443", "hub.test:443:443"] {
            assert!(verify_http(&request(target), "hub.test", 443, &credentials()).is_err());
        }
        let mut wrong = credentials(); wrong.password = "replacement".into();
        assert_eq!(verify_http(&request("hub.test:443"), "hub.test", 443, &wrong), Err("ssh_proxy_auth"));
    }
    #[test] fn rejects_body_headers_duplicate_auth_and_large_handshakes() {
        let valid = String::from_utf8(request("hub.test:443")).unwrap();
        for header in ["Content-Length: 1", "Transfer-Encoding: chunked", "Proxy-Authorization: Basic bG9jYWw6c2VjcmV0", " folded"] {
            let text = valid.replace("\r\n\r\n", &format!("\r\n{header}\r\n\r\n"));
            assert!(verify_http(text.as_bytes(), "hub.test", 443, &credentials()).is_err());
        }
        assert!(verify_http(&vec![b'a'; MAX_HANDSHAKE + 1], "hub.test", 443, &credentials()).is_err());
    }
    #[test] fn ipv6_authority_is_structural() {
        assert!(verify_http(&request("[::1]:443"), "0:0:0:0:0:0:0:1", 443, &credentials()).is_ok());
        assert!(verify_http(&request("::1:443"), "::1", 443, &credentials()).is_err());
    }
    #[tokio::test] async fn socks_rejects_unauthenticated_clients() {
        let (mut client, mut server) = tokio::io::duplex(128);
        client.write_all(&[5, 1, 0]).await.unwrap();
        assert!(matches!(handshake(&mut server, "hub.test", 443, &[], &credentials()).await, Err("ssh_proxy_auth")));
        let mut reply = [0; 2]; client.read_exact(&mut reply).await.unwrap(); assert_eq!(reply, [5, 255]);
    }
    #[tokio::test] async fn socks_exact_target_does_not_consume_tls_bytes() {
        let (mut client, mut server) = tokio::io::duplex(512);
        let mut request = socks_request(b"hub.test", 3, 443); request.extend([22, 3, 3]);
        client.write_all(&request).await.unwrap();
        assert!(matches!(handshake(&mut server, "hub.test", 443, &[], &credentials()).await, Ok(Protocol::Socks)));
        let mut tls = [0; 3]; server.read_exact(&mut tls).await.unwrap(); assert_eq!(tls, [22, 3, 3]);
    }
    #[tokio::test] async fn socks_captured_address_is_authorized_only_for_its_fixed_hub_authority() {
        let captured = ["192.0.2.10".parse::<IpAddr>().unwrap()];
        let socks_bytes = socks_request(&[192, 0, 2, 10], 1, 443);
        let (mut client, mut server) = tokio::io::duplex(512); client.write_all(&socks_bytes).await.unwrap();
        assert!(matches!(handshake(&mut server, "hub.test", 443, &captured, &credentials()).await, Ok(Protocol::Socks)));

        for (host, address_type, port) in [
            (&[192, 0, 2, 11][..], 1, 443),
            (&[192, 0, 2, 10][..], 1, 8443),
            (b"other.test" as &[u8], 3, 443),
        ] {
            let request = socks_request(host, address_type, port);
            let (mut client, mut server) = tokio::io::duplex(512); client.write_all(&request).await.unwrap();
            assert!(matches!(handshake(&mut server, "hub.test", 443, &captured, &credentials()).await, Err("ssh_proxy_target")));
        }
        assert!(verify_http(&request("192.0.2.10:443"), "hub.test", 443, &credentials()).is_err());
    }
    #[tokio::test] async fn socks_rejects_bind_udp_other_target_and_bad_password() {
        for (command, host, password) in [(2, "hub.test", "secret"), (3, "hub.test", "secret"), (1, "evil.com", "secret"), (1, "hub.test", "wrong!")] {
            let (mut client, mut server) = tokio::io::duplex(512);
            let mut request = vec![5, 1, 2, 1, 5]; request.extend(b"local"); request.push(password.len() as u8); request.extend(password.as_bytes());
            request.extend([5, command, 0, 3, host.len() as u8]); request.extend(host.as_bytes()); request.extend([1, 187]);
            client.write_all(&request).await.unwrap();
            assert!(handshake(&mut server, "hub.test", 443, &[], &credentials()).await.is_err());
        }
    }
    #[tokio::test] async fn oversized_and_incomplete_http_cannot_allocate_or_wait_unboundedly() {
        let (mut client, mut server) = tokio::io::duplex(MAX_HANDSHAKE * 2);
        client.write_all(&vec![b'C'; MAX_HANDSHAKE + 1]).await.unwrap();
        assert!(matches!(handshake(&mut server, "hub.test", 443, &[], &credentials()).await, Err("ssh_proxy_handshake")));
        let (mut client, mut server) = tokio::io::duplex(128); client.write_all(b"CONNECT ").await.unwrap();
        assert!(tokio::time::timeout(std::time::Duration::from_millis(20), handshake(&mut server, "hub.test", 443, &[], &credentials())).await.is_err());
    }
}
