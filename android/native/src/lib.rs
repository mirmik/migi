use jni::JNIEnv;
use jni::objects::{JClass, JObject, JString, JValue};
use jni::sys::{jint, jlong, jstring};
use quiche::h3::NameValue;
use rand::RngCore;
use sha2::{Digest, Sha256};
use std::error::Error;
use std::fs::File;
use std::io::{self, Read, Write};
use std::net::{SocketAddr, ToSocketAddrs, UdpSocket};
use std::os::fd::{FromRawFd, RawFd};
use std::time::{Duration, Instant};
use url::Url;

const MAX_DATAGRAM_SIZE: usize = 1350;
const CALLBACK_POLL: Duration = Duration::from_millis(100);

unsafe extern "C" {
    fn dup(oldfd: RawFd) -> RawFd;
}

type AnyError = Box<dyn Error + Send + Sync>;

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_run(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    device_id: JString,
    certificate_pin: JString,
    credential: JString,
    callback: JObject,
) -> jstring {
    let result = (|| -> Result<(), AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let device_id: String = env.get_string(&device_id)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        run_client(
            &mut env,
            &callback,
            &endpoint,
            &device_id,
            &expected_pin,
            &credential,
        )
    })();

    match result {
        Ok(()) => std::ptr::null_mut(),
        Err(error) => env
            .new_string(error.to_string())
            .map(JString::into_raw)
            .unwrap_or(std::ptr::null_mut()),
    }
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_pair(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    secret: JString,
    device_id: JString,
    device_name: JString,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let secret: String = env.get_string(&secret)?.into();
        let device_id: String = env.get_string(&device_id)?.into();
        let device_name: String = env.get_string(&device_name)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        let body = serde_json::json!({
            "secret": secret,
            "device_id": device_id,
            "name": device_name,
        })
        .to_string();
        small_request(
            &endpoint,
            &expected_pin,
            "POST",
            "/v1/pair",
            Some(body.as_bytes()),
            None,
            "201",
        )
    })();

    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_releaseMetadata(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    credential: JString,
    artifact_id: JString,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let artifact_id: String = env.get_string(&artifact_id)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        validate_artifact_id(&artifact_id)?;
        small_request(
            &endpoint,
            &expected_pin,
            "GET",
            &format!("/v1/releases/{artifact_id}"),
            None,
            Some(&credential),
            "200",
        )
    })();
    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_downloadRelease(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    credential: JString,
    artifact_id: JString,
    file_descriptor: jint,
    max_bytes: jlong,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let artifact_id: String = env.get_string(&artifact_id)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        validate_artifact_id(&artifact_id)?;
        if file_descriptor < 0 || max_bytes <= 0 {
            return Err(invalid("valid destination descriptor and size limit are required"));
        }
        let duplicated = unsafe { dup(file_descriptor) };
        if duplicated < 0 {
            return Err(io::Error::last_os_error().into());
        }
        let mut destination = unsafe { File::from_raw_fd(duplicated) };
        download_request(
            &endpoint,
            &expected_pin,
            &credential,
            &format!("/v1/releases/{artifact_id}/apk"),
            &mut destination,
            max_bytes as u64,
        )
    })();
    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_listSharedFiles(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    credential: JString,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        small_request(
            &endpoint,
            &expected_pin,
            "GET",
            "/v1/files",
            None,
            Some(&credential),
            "200",
        )
    })();
    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_uploadSharedFile(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    credential: JString,
    name: JString,
    mime: JString,
    file_descriptor: jint,
    size: jlong,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let name: String = env.get_string(&name)?.into();
        let mime: String = env.get_string(&mime)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        validate_shared_file_name(&name)?;
        validate_mime(&mime)?;
        if file_descriptor < 0 || size <= 0 {
            return Err(invalid("valid source descriptor and file size are required"));
        }
        let duplicated = unsafe { dup(file_descriptor) };
        if duplicated < 0 {
            return Err(io::Error::last_os_error().into());
        }
        let mut source = unsafe { File::from_raw_fd(duplicated) };
        upload_request(
            &endpoint,
            &expected_pin,
            &credential,
            &name,
            &mime,
            &mut source,
            size as u64,
        )
    })();
    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_downloadSharedFile(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    certificate_pin: JString,
    credential: JString,
    file_id: JString,
    file_descriptor: jint,
    max_bytes: jlong,
) -> jstring {
    let result = (|| -> Result<String, AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let credential: String = env.get_string(&credential)?.into();
        let file_id: String = env.get_string(&file_id)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        validate_token(&credential)?;
        validate_shared_file_id(&file_id)?;
        if file_descriptor < 0 || max_bytes <= 0 {
            return Err(invalid("valid destination descriptor and size limit are required"));
        }
        let duplicated = unsafe { dup(file_descriptor) };
        if duplicated < 0 {
            return Err(io::Error::last_os_error().into());
        }
        let mut destination = unsafe { File::from_raw_fd(duplicated) };
        download_request(
            &endpoint,
            &expected_pin,
            &credential,
            &format!("/v1/files/{file_id}/content"),
            &mut destination,
            max_bytes as u64,
        )
    })();
    let response = match result {
        Ok(body) => body,
        Err(error) => format!("MIGI_ERROR:{error}"),
    };
    env.new_string(response)
        .map(JString::into_raw)
        .unwrap_or(std::ptr::null_mut())
}

fn run_client(
    env: &mut JNIEnv,
    callback: &JObject,
    endpoint: &str,
    device_id: &str,
    expected_pin: &[u8; 32],
    credential: &str,
) -> Result<(), AnyError> {
    let url = Url::parse(endpoint)?;
    if url.scheme() != "https" {
        return Err(invalid("endpoint must use https"));
    }
    let host = url
        .host_str()
        .ok_or_else(|| invalid("endpoint has no host"))?;
    let port = url.port_or_known_default().unwrap_or(443);
    let peer_addr = (host, port)
        .to_socket_addrs()?
        .next()
        .ok_or_else(|| invalid("endpoint host did not resolve"))?;
    let bind_addr = match peer_addr {
        SocketAddr::V4(_) => "0.0.0.0:0",
        SocketAddr::V6(_) => "[::]:0",
    };
    let socket = UdpSocket::bind(bind_addr)?;
    socket.set_read_timeout(Some(CALLBACK_POLL))?;
    let local_addr = socket.local_addr()?;

    let mut config = quic_config()?;

    let mut scid_bytes = [0_u8; quiche::MAX_CONN_ID_LEN];
    rand::rng().fill_bytes(&mut scid_bytes);
    let scid = quiche::ConnectionId::from_ref(&scid_bytes);
    let server_name = url.domain();
    let mut connection = quiche::connect(server_name, &scid, local_addr, peer_addr, &mut config)?;
    let h3_config = quiche::h3::Config::new()?;
    let mut h3_connection = None;
    let mut certificate_checked = false;
    let mut event_stream = None;
    let mut response_accepted = false;
    let mut pending = Vec::<u8>::new();
    let mut input = [0_u8; 65_535];
    let mut output = [0_u8; MAX_DATAGRAM_SIZE];

    callback_state(env, callback, "Connecting over native QUIC")?;

    loop {
        if callback_closed(env, callback)? {
			let _ = connection.close(true, 0, b"client stopped");
			flush_packets(&socket, &mut connection, &mut output)?;
            return Ok(());
        }

        flush_packets(&socket, &mut connection, &mut output)?;

        match socket.recv_from(&mut input) {
            Ok((length, from)) => {
                let info = quiche::RecvInfo {
                    from,
                    to: local_addr,
                };
                match connection.recv(&mut input[..length], info) {
                    Ok(_) | Err(quiche::Error::Done) => {}
                    Err(error) => return Err(format!("QUIC receive failed: {error:?}").into()),
                }
            }
            Err(error)
                if matches!(
                    error.kind(),
                    io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
                ) =>
            {
                if connection.timeout() == Some(Duration::ZERO) {
                    connection.on_timeout();
                }
            }
            Err(error) => return Err(error.into()),
        }

        if connection.is_closed() {
            return Err(format!(
                "QUIC connection closed: local={:?}, peer={:?}",
                connection.local_error(),
                connection.peer_error()
            )
            .into());
        }

        if connection.is_established() && !certificate_checked {
            let peer_certificate = connection
                .peer_cert()
                .ok_or_else(|| invalid("server did not present a certificate"))?;
            let actual_pin: [u8; 32] = Sha256::digest(peer_certificate).into();
            if &actual_pin != expected_pin {
                let _ = connection.close(true, 0x100, b"certificate pin mismatch");
                return Err(format!(
                    "server certificate pin mismatch (received {})",
                    format_pin(&actual_pin)
                )
                .into());
            }
            certificate_checked = true;
            callback_state(env, callback, "Server certificate pin verified")?;
            h3_connection = Some(quiche::h3::Connection::with_transport(
                &mut connection,
                &h3_config,
            )?);
        }

        if certificate_checked && event_stream.is_none() {
            let headers = request_headers("GET", &url, "/v1/events", None, Some(credential));
            event_stream = Some(
                h3_connection
                    .as_mut()
                    .expect("HTTP/3 initialized after certificate check")
                    .send_request(&mut connection, &headers, true)?,
            );
        }

        let mut acknowledgements = Vec::new();
        if let Some(http3) = h3_connection.as_mut() {
            loop {
                match http3.poll(&mut connection) {
                    Ok((stream_id, quiche::h3::Event::Headers { list, .. })) => {
                        if Some(stream_id) == event_stream {
                            let status = list
                                .iter()
                                .find(|header| header.name() == b":status")
                                .and_then(|header| std::str::from_utf8(header.value()).ok());
                            if status != Some("200") {
                                return Err(format!(
                                    "event stream returned HTTP {}",
                                    status.unwrap_or("unknown")
                                )
                                .into());
                            }
                            if !response_accepted {
                                response_accepted = true;
                                callback_state(
                                    env,
                                    callback,
                                    "Connected with h3 (certificate pinned)",
                                )?;
                            }
                        }
                    }
                    Ok((stream_id, quiche::h3::Event::Data)) => {
                        while let Ok(read) = http3.recv_body(&mut connection, stream_id, &mut input)
                        {
                            if Some(stream_id) == event_stream {
                                pending.extend_from_slice(&input[..read]);
                                consume_lines(env, callback, &mut pending, &mut acknowledgements)?;
                            }
                        }
                    }
                    Ok((stream_id, quiche::h3::Event::Finished))
                        if Some(stream_id) == event_stream =>
                    {
                        return Err("event stream ended".into());
                    }
                    Ok((stream_id, quiche::h3::Event::Reset(code)))
                        if Some(stream_id) == event_stream =>
                    {
                        return Err(format!("event stream was reset ({code})").into());
                    }
                    Ok((_, _)) => {}
                    Err(quiche::h3::Error::Done) => break,
                    Err(error) => return Err(format!("HTTP/3 processing failed: {error:?}").into()),
                }
            }

            for through in acknowledgements {
                send_ack(http3, &mut connection, &url, device_id, credential, through)?;
            }
        }
    }
}

fn small_request(
    endpoint: &str,
    expected_pin: &[u8; 32],
    method: &str,
    path: &str,
    body: Option<&[u8]>,
    bearer: Option<&str>,
    expected_status: &str,
) -> Result<String, AnyError> {
    let url = Url::parse(endpoint)?;
    if url.scheme() != "https" {
        return Err(invalid("endpoint must use https"));
    }
    let host = url
        .host_str()
        .ok_or_else(|| invalid("endpoint has no host"))?;
    let port = url.port_or_known_default().unwrap_or(443);
    let peer_addr = (host, port)
        .to_socket_addrs()?
        .next()
        .ok_or_else(|| invalid("endpoint host did not resolve"))?;
    let bind_addr = match peer_addr {
        SocketAddr::V4(_) => "0.0.0.0:0",
        SocketAddr::V6(_) => "[::]:0",
    };
    let socket = UdpSocket::bind(bind_addr)?;
    socket.set_read_timeout(Some(CALLBACK_POLL))?;
    let local_addr = socket.local_addr()?;
    let mut config = quic_config()?;
    let mut scid_bytes = [0_u8; quiche::MAX_CONN_ID_LEN];
    rand::rng().fill_bytes(&mut scid_bytes);
    let scid = quiche::ConnectionId::from_ref(&scid_bytes);
    let h3_config = quiche::h3::Config::new()?;
    let mut connection = quiche::connect(url.domain(), &scid, local_addr, peer_addr, &mut config)?;
    let mut http3 = None;
    let mut certificate_checked = false;
    let mut request_stream = None;
    let mut response_status = None::<String>;
    let mut response_body = Vec::new();
    let mut input = [0_u8; 65_535];
    let mut output = [0_u8; MAX_DATAGRAM_SIZE];
    let deadline = Instant::now() + Duration::from_secs(15);

    let result = (|| -> Result<String, AnyError> {
        loop {
            if Instant::now() >= deadline {
                return Err("request timed out".into());
            }
            flush_packets(&socket, &mut connection, &mut output)?;
            match socket.recv_from(&mut input) {
                Ok((length, from)) => {
                    let info = quiche::RecvInfo {
                        from,
                        to: local_addr,
                    };
                    match connection.recv(&mut input[..length], info) {
                        Ok(_) | Err(quiche::Error::Done) => {}
                        Err(error) => return Err(format!("QUIC receive failed: {error:?}").into()),
                    }
                }
                Err(error)
                    if matches!(
                        error.kind(),
                        io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
                    ) =>
                {
                    if connection.timeout() == Some(Duration::ZERO) {
                        connection.on_timeout();
                    }
                }
                Err(error) => return Err(error.into()),
            }
            if connection.is_closed() {
                return Err(format!(
                    "QUIC connection closed during request: local={:?}, peer={:?}",
                    connection.local_error(),
                    connection.peer_error()
                )
                .into());
            }
            if connection.is_established() && !certificate_checked {
                verify_certificate_pin(&connection, expected_pin)?;
                certificate_checked = true;
                http3 = Some(quiche::h3::Connection::with_transport(
                    &mut connection,
                    &h3_config,
                )?);
            }
            if certificate_checked && request_stream.is_none() {
                let headers =
                    request_headers(method, &url, path, body.map(|value| value.len()), bearer);
                let stream = http3
                    .as_mut()
                    .unwrap()
                    .send_request(&mut connection, &headers, body.is_none())?;
                if let Some(body) = body {
                    http3
                        .as_mut()
                        .unwrap()
                        .send_body(&mut connection, stream, body, true)?;
                }
                request_stream = Some(stream);
            }
            if let Some(http3) = http3.as_mut() {
                loop {
                    match http3.poll(&mut connection) {
                        Ok((stream, quiche::h3::Event::Headers { list, .. }))
                            if Some(stream) == request_stream =>
                        {
                            response_status = list
                                .iter()
                                .find(|header| header.name() == b":status")
                                .and_then(|header| std::str::from_utf8(header.value()).ok())
                                .map(str::to_owned);
                        }
                        Ok((stream, quiche::h3::Event::Data)) if Some(stream) == request_stream => {
                            while let Ok(read) =
                                http3.recv_body(&mut connection, stream, &mut input)
                            {
                                if response_body.len() + read > 256 * 1024 {
                                    return Err("response is too large".into());
                                }
                                response_body.extend_from_slice(&input[..read]);
                            }
                        }
                        Ok((stream, quiche::h3::Event::Finished))
                            if Some(stream) == request_stream =>
                        {
                            let status = response_status.as_deref().unwrap_or("unknown");
                            let body = String::from_utf8(response_body)?;
                            if status != expected_status {
                                return Err(format!(
                                    "request returned HTTP {status}: {}",
                                    body.trim()
                                )
                                .into());
                            }
                            return Ok(body);
                        }
                        Ok((stream, quiche::h3::Event::Reset(code)))
                            if Some(stream) == request_stream =>
                        {
                            return Err(format!("request stream was reset ({code})").into());
                        }
                        Ok((_, _)) => {}
                        Err(quiche::h3::Error::Done) => break,
                        Err(error) => return Err(format!("HTTP/3 request failed: {error:?}").into()),
                    }
                }
            }
        }
    })();
    finish_with_cleanup(result, || {
        close_short_connection(&socket, &mut connection, &mut output)
    })
}

fn upload_request(
    endpoint: &str,
    expected_pin: &[u8; 32],
    credential: &str,
    name: &str,
    mime: &str,
    source: &mut File,
    size: u64,
) -> Result<String, AnyError> {
    let url = Url::parse(endpoint)?;
    if url.scheme() != "https" {
        return Err(invalid("endpoint must use https"));
    }
    let host = url.host_str().ok_or_else(|| invalid("endpoint has no host"))?;
    let port = url.port_or_known_default().unwrap_or(443);
    let peer_addr = (host, port)
        .to_socket_addrs()?
        .next()
        .ok_or_else(|| invalid("endpoint host did not resolve"))?;
    let bind_addr = match peer_addr {
        SocketAddr::V4(_) => "0.0.0.0:0",
        SocketAddr::V6(_) => "[::]:0",
    };
    let socket = UdpSocket::bind(bind_addr)?;
    socket.set_read_timeout(Some(CALLBACK_POLL))?;
    let local_addr = socket.local_addr()?;
    let mut config = quic_config()?;
    let mut scid_bytes = [0_u8; quiche::MAX_CONN_ID_LEN];
    rand::rng().fill_bytes(&mut scid_bytes);
    let scid = quiche::ConnectionId::from_ref(&scid_bytes);
    let h3_config = quiche::h3::Config::new()?;
    let mut connection = quiche::connect(url.domain(), &scid, local_addr, peer_addr, &mut config)?;
    let mut http3 = None;
    let mut certificate_checked = false;
    let mut request_stream = None;
    let mut response_status = None::<String>;
    let mut response_body = Vec::new();
    let mut sent = 0_u64;
    let mut pending = Vec::<u8>::new();
    let mut pending_offset = 0_usize;
    let mut input = [0_u8; 65_535];
    let mut output = [0_u8; MAX_DATAGRAM_SIZE];
    let deadline = Instant::now() + Duration::from_secs(15 * 60);

    let result = (|| -> Result<String, AnyError> {
        loop {
            if Instant::now() >= deadline {
                return Err("file upload timed out".into());
            }
            flush_packets(&socket, &mut connection, &mut output)?;
            match socket.recv_from(&mut input) {
                Ok((length, from)) => {
                    let info = quiche::RecvInfo { from, to: local_addr };
                    match connection.recv(&mut input[..length], info) {
                        Ok(_) | Err(quiche::Error::Done) => {}
                        Err(error) => return Err(format!("QUIC receive failed: {error:?}").into()),
                    }
                }
                Err(error)
                    if matches!(error.kind(), io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut) =>
                {
                    if connection.timeout() == Some(Duration::ZERO) {
                        connection.on_timeout();
                    }
                }
                Err(error) => return Err(error.into()),
            }
            if connection.is_closed() {
                return Err("QUIC connection closed during file upload".into());
            }
            if connection.is_established() && !certificate_checked {
                verify_certificate_pin(&connection, expected_pin)?;
                certificate_checked = true;
                http3 = Some(quiche::h3::Connection::with_transport(
                    &mut connection,
                    &h3_config,
                )?);
            }
            if certificate_checked && request_stream.is_none() {
                let mut headers = request_headers(
                    "POST",
                    &url,
                    "/v1/files",
                    None,
                    Some(credential),
                );
                headers.push(quiche::h3::Header::new(b"content-type", mime.as_bytes()));
                headers.push(quiche::h3::Header::new(
                    b"content-length",
                    size.to_string().as_bytes(),
                ));
                headers.push(quiche::h3::Header::new(
                    b"x-migi-filename",
                    name.as_bytes(),
                ));
                request_stream = Some(
                    http3
                        .as_mut()
                        .unwrap()
                        .send_request(&mut connection, &headers, false)?,
                );
            }
            if let (Some(http3), Some(stream)) = (http3.as_mut(), request_stream) {
                if sent < size {
                    if pending_offset == pending.len() {
                        let remaining = (size - sent).min(64 * 1024) as usize;
                        pending.resize(remaining, 0);
                        source.read_exact(&mut pending)?;
                        pending_offset = 0;
                    }
                    let final_chunk = sent + (pending.len() - pending_offset) as u64 == size;
                    match http3.send_body(
                        &mut connection,
                        stream,
                        &pending[pending_offset..],
                        final_chunk,
                    ) {
                        Ok(written) => {
                            pending_offset += written;
                            sent += written as u64;
                        }
                        Err(quiche::h3::Error::Done) => {}
                        Err(error) => return Err(format!("HTTP/3 upload failed: {error:?}").into()),
                    }
                }
                loop {
                    match http3.poll(&mut connection) {
                        Ok((event_stream, quiche::h3::Event::Headers { list, .. }))
                            if event_stream == stream =>
                        {
                            response_status = header_value(&list, b":status");
                        }
                        Ok((event_stream, quiche::h3::Event::Data)) if event_stream == stream => {
                            while let Ok(read) = http3.recv_body(&mut connection, stream, &mut input) {
                                if response_body.len() + read > 256 * 1024 {
                                    return Err("response is too large".into());
                                }
                                response_body.extend_from_slice(&input[..read]);
                            }
                        }
                        Ok((event_stream, quiche::h3::Event::Finished)) if event_stream == stream => {
                            if sent != size {
                                return Err(invalid("server finished before upload completed"));
                            }
                            let status = response_status.as_deref().unwrap_or("unknown");
                            let body = String::from_utf8(response_body)?;
                            if status != "201" {
                                return Err(format!(
                                    "file upload returned HTTP {status}: {}",
                                    body.trim()
                                )
                                .into());
                            }
                            return Ok(body);
                        }
                        Ok((event_stream, quiche::h3::Event::Reset(code)))
                            if event_stream == stream =>
                        {
                            return Err(format!("file upload stream was reset ({code})").into());
                        }
                        Ok((_, _)) => {}
                        Err(quiche::h3::Error::Done) => break,
                        Err(error) => return Err(format!("HTTP/3 upload failed: {error:?}").into()),
                    }
                }
            }
        }
    })();
    finish_with_cleanup(result, || {
        close_short_connection(&socket, &mut connection, &mut output)
    })
}

fn download_request(
    endpoint: &str,
    expected_pin: &[u8; 32],
    credential: &str,
    request_path: &str,
    destination: &mut File,
    max_bytes: u64,
) -> Result<String, AnyError> {
    let url = Url::parse(endpoint)?;
    if url.scheme() != "https" {
        return Err(invalid("endpoint must use https"));
    }
    let host = url.host_str().ok_or_else(|| invalid("endpoint has no host"))?;
    let port = url.port_or_known_default().unwrap_or(443);
    let peer_addr = (host, port)
        .to_socket_addrs()?
        .next()
        .ok_or_else(|| invalid("endpoint host did not resolve"))?;
    let bind_addr = match peer_addr {
        SocketAddr::V4(_) => "0.0.0.0:0",
        SocketAddr::V6(_) => "[::]:0",
    };
    let socket = UdpSocket::bind(bind_addr)?;
    socket.set_read_timeout(Some(CALLBACK_POLL))?;
    let local_addr = socket.local_addr()?;
    let mut config = quic_config()?;
    let mut scid_bytes = [0_u8; quiche::MAX_CONN_ID_LEN];
    rand::rng().fill_bytes(&mut scid_bytes);
    let scid = quiche::ConnectionId::from_ref(&scid_bytes);
    let h3_config = quiche::h3::Config::new()?;
    let mut connection = quiche::connect(url.domain(), &scid, local_addr, peer_addr, &mut config)?;
    let mut http3 = None;
    let mut certificate_checked = false;
    let mut request_stream = None;
    let mut expected_length = None::<u64>;
    let mut expected_digest = None::<String>;
    let mut received = 0_u64;
    let mut digest = Sha256::new();
    let mut input = [0_u8; 65_535];
    let mut output = [0_u8; MAX_DATAGRAM_SIZE];
    let deadline = Instant::now() + Duration::from_secs(15 * 60);

    let result = (|| -> Result<String, AnyError> {
        loop {
            if Instant::now() >= deadline {
                return Err("artifact download timed out".into());
            }
            flush_packets(&socket, &mut connection, &mut output)?;
            match socket.recv_from(&mut input) {
                Ok((length, from)) => {
                    let info = quiche::RecvInfo {
                        from,
                        to: local_addr,
                    };
                    match connection.recv(&mut input[..length], info) {
                        Ok(_) | Err(quiche::Error::Done) => {}
                        Err(error) => return Err(format!("QUIC receive failed: {error:?}").into()),
                    }
                }
                Err(error)
                    if matches!(
                        error.kind(),
                        io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
                    ) =>
                {
                    if connection.timeout() == Some(Duration::ZERO) {
                        connection.on_timeout();
                    }
                }
                Err(error) => return Err(error.into()),
            }
            if connection.is_closed() {
                return Err("QUIC connection closed during artifact download".into());
            }
            if connection.is_established() && !certificate_checked {
                verify_certificate_pin(&connection, expected_pin)?;
                certificate_checked = true;
                http3 = Some(quiche::h3::Connection::with_transport(
                    &mut connection,
                    &h3_config,
                )?);
            }
            if certificate_checked && request_stream.is_none() {
                let headers = request_headers(
                    "GET",
                    &url,
                    request_path,
                    None,
                    Some(credential),
                );
                request_stream = Some(
                    http3
                        .as_mut()
                        .unwrap()
                        .send_request(&mut connection, &headers, true)?,
                );
            }
            if let Some(http3) = http3.as_mut() {
                loop {
                    match http3.poll(&mut connection) {
                        Ok((stream, quiche::h3::Event::Headers { list, .. }))
                            if Some(stream) == request_stream =>
                        {
                            let response_status = header_value(&list, b":status");
                            expected_length = header_value(&list, b"content-length")
                                .map(|value| value.parse::<u64>())
                                .transpose()?;
                            expected_digest = header_value(&list, b"x-content-sha256");
                            if response_status.as_deref() != Some("200") {
                                return Err(format!(
                                    "artifact download returned HTTP {}",
                                    response_status.as_deref().unwrap_or("unknown")
                                )
                                .into());
                            }
                            let length = expected_length
                                .ok_or_else(|| invalid("artifact response has no content length"))?;
                            if length == 0 || length > max_bytes {
                                return Err(invalid("artifact response exceeds configured size"));
                            }
                        }
                        Ok((stream, quiche::h3::Event::Data)) if Some(stream) == request_stream => {
                            while let Ok(read) = http3.recv_body(&mut connection, stream, &mut input) {
                                received += read as u64;
                                if received > max_bytes
                                    || expected_length.is_some_and(|value| received > value)
                                {
                                    return Err(invalid("artifact body exceeds declared size"));
                                }
                                destination.write_all(&input[..read])?;
                                digest.update(&input[..read]);
                            }
                        }
                        Ok((stream, quiche::h3::Event::Finished)) if Some(stream) == request_stream => {
                            let length = expected_length
                                .ok_or_else(|| invalid("artifact response has no content length"))?;
                            if received != length {
                                return Err(invalid(
                                    "artifact byte count differs from content length",
                                ));
                            }
                            destination.sync_all()?;
                            let actual_digest = hex_lower(&digest.finalize());
                            if expected_digest.as_deref() != Some(actual_digest.as_str()) {
                                return Err(invalid("artifact digest differs from response header"));
                            }
                            return Ok(serde_json::json!({
                                "bytes": received,
                                "sha256": actual_digest,
                            })
                            .to_string());
                        }
                        Ok((stream, quiche::h3::Event::Reset(code)))
                            if Some(stream) == request_stream =>
                        {
                            return Err(format!("artifact stream was reset ({code})").into());
                        }
                        Ok((_, _)) => {}
                        Err(quiche::h3::Error::Done) => break,
                        Err(error) => {
                            return Err(format!("HTTP/3 download failed: {error:?}").into());
                        }
                    }
                }
            }
        }
    })();
    finish_with_cleanup(result, || {
        close_short_connection(&socket, &mut connection, &mut output)
    })
}

fn quic_config() -> Result<quiche::Config, quiche::Error> {
    let mut config = quiche::Config::new(quiche::PROTOCOL_VERSION)?;
    // Authentication is performed by verify_certificate_pin() before HTTP/3
    // is created or application data is sent.
    config.verify_peer(false);
    config.set_application_protos(quiche::h3::APPLICATION_PROTOCOL)?;
    config.set_max_idle_timeout(90_000);
    config.set_max_recv_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_max_send_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_initial_max_data(512 * 1024 * 1024);
    config.set_initial_max_stream_data_bidi_local(1_000_000);
    config.set_initial_max_stream_data_bidi_remote(512 * 1024 * 1024);
    config.set_initial_max_stream_data_uni(1_000_000);
    config.set_initial_max_streams_bidi(100);
    config.set_initial_max_streams_uni(100);
    Ok(config)
}

fn verify_certificate_pin(
    connection: &quiche::Connection,
    expected_pin: &[u8; 32],
) -> Result<(), AnyError> {
    let peer_certificate = connection
        .peer_cert()
        .ok_or_else(|| invalid("server did not present a certificate"))?;
    let actual_pin: [u8; 32] = Sha256::digest(peer_certificate).into();
    if &actual_pin != expected_pin {
        return Err(format!(
            "server certificate pin mismatch (received {})",
            format_pin(&actual_pin)
        )
        .into());
    }
    Ok(())
}

fn flush_packets(
    socket: &UdpSocket,
    connection: &mut quiche::Connection,
    output: &mut [u8],
) -> Result<(), AnyError> {
    loop {
        match connection.send(output) {
            Ok((written, info)) => {
                socket.send_to(&output[..written], info.to)?;
            }
            Err(quiche::Error::Done) => return Ok(()),
            Err(error) => return Err(format!("QUIC send failed: {error:?}").into()),
        }
    }
}

fn close_short_connection(
    socket: &UdpSocket,
    connection: &mut quiche::Connection,
    output: &mut [u8],
) {
    if connection.is_closed() {
        return;
    }
    if connection
        .close(true, 0, b"short request complete")
        .is_ok()
    {
        let _ = flush_packets(socket, connection, output);
    }
}

fn finish_with_cleanup<T>(
    result: Result<T, AnyError>,
    cleanup: impl FnOnce(),
) -> Result<T, AnyError> {
    cleanup();
    result
}

fn request_headers<'a>(
    method: &'a str,
    url: &'a Url,
    path: &'a str,
    content_length: Option<usize>,
    bearer: Option<&str>,
) -> Vec<quiche::h3::Header> {
    let host = url.host_str().unwrap_or_default();
    let mut authority = if host.contains(':') {
        format!("[{host}]")
    } else {
        host.to_owned()
    };
    if let Some(port) = url.port() {
        authority = format!("{authority}:{port}");
    }
    let mut headers = vec![
        quiche::h3::Header::new(b":method", method.as_bytes()),
        quiche::h3::Header::new(b":scheme", b"https"),
        quiche::h3::Header::new(b":authority", authority.as_bytes()),
        quiche::h3::Header::new(b":path", path.as_bytes()),
        quiche::h3::Header::new(b"user-agent", b"migi-quiche/0.1"),
    ];
    if let Some(length) = content_length {
        headers.push(quiche::h3::Header::new(
            b"content-type",
            b"application/json",
        ));
        headers.push(quiche::h3::Header::new(
            b"content-length",
            length.to_string().as_bytes(),
        ));
    }
    if let Some(token) = bearer {
        headers.push(quiche::h3::Header::new(
            b"authorization",
            format!("Bearer {token}").as_bytes(),
        ));
    }
    headers
}

fn header_value(headers: &[quiche::h3::Header], name: &[u8]) -> Option<String> {
    headers
        .iter()
        .find(|header| header.name() == name)
        .and_then(|header| std::str::from_utf8(header.value()).ok())
        .map(str::to_owned)
}

fn send_ack(
    http3: &mut quiche::h3::Connection,
    connection: &mut quiche::Connection,
    url: &Url,
    device_id: &str,
    credential: &str,
    through: i64,
) -> Result<(), AnyError> {
    let body = format!(r#"{{"device_id":"{device_id}","through":{through}}}"#);
    let headers = request_headers("POST", url, "/v1/ack", Some(body.len()), Some(credential));
    let stream = http3.send_request(connection, &headers, false)?;
    http3.send_body(connection, stream, body.as_bytes(), true)?;
    Ok(())
}

fn consume_lines(
    env: &mut JNIEnv,
    callback: &JObject,
    pending: &mut Vec<u8>,
    acknowledgements: &mut Vec<i64>,
) -> Result<(), AnyError> {
    while let Some(newline) = pending.iter().position(|byte| *byte == b'\n') {
        let mut bytes: Vec<u8> = pending.drain(..=newline).collect();
        bytes.pop();
        if bytes.last() == Some(&b'\r') {
            bytes.pop();
        }
        if bytes.is_empty() {
            continue;
        }
        let line = String::from_utf8(bytes)?;
        let java_line = env.new_string(line)?;
        let java_line_object = JObject::from(java_line);
        let through = env
            .call_method(
                callback,
                "onLine",
                "(Ljava/lang/String;)J",
                &[JValue::Object(&java_line_object)],
            )?
            .j()?;
        if through > 0 {
            acknowledgements.push(through);
        }
    }
    Ok(())
}

fn callback_state(env: &mut JNIEnv, callback: &JObject, state: &str) -> Result<(), AnyError> {
    let java_state = env.new_string(state)?;
    let java_state_object = JObject::from(java_state);
    env.call_method(
        callback,
        "onState",
        "(Ljava/lang/String;)V",
        &[JValue::Object(&java_state_object)],
    )?;
    Ok(())
}

fn callback_closed(env: &mut JNIEnv, callback: &JObject) -> Result<bool, AnyError> {
    Ok(env.call_method(callback, "isClosed", "()Z", &[])?.z()?)
}

fn parse_pin(raw: &str) -> Result<[u8; 32], AnyError> {
    let compact: String = raw
        .chars()
        .filter(|character| !character.is_ascii_whitespace() && *character != ':')
        .collect();
    if compact.len() != 64
        || !compact
            .chars()
            .all(|character| character.is_ascii_hexdigit())
    {
        return Err(invalid(
            "certificate pin must contain 64 hexadecimal digits",
        ));
    }
    let mut pin = [0_u8; 32];
    for (index, byte) in pin.iter_mut().enumerate() {
        *byte = u8::from_str_radix(&compact[index * 2..index * 2 + 2], 16)?;
    }
    Ok(pin)
}

fn format_pin(pin: &[u8; 32]) -> String {
    pin.iter()
        .map(|byte| format!("{byte:02X}"))
        .collect::<Vec<_>>()
        .join(":")
}

fn validate_token(token: &str) -> Result<(), AnyError> {
    if token.len() != 43
        || !token.chars().all(|character| {
            character.is_ascii_alphanumeric() || character == '-' || character == '_'
        })
    {
        return Err(invalid("device credential is malformed"));
    }
    Ok(())
}

fn validate_artifact_id(value: &str) -> Result<(), AnyError> {
    if value.len() != 32
        || !value.chars().all(|character| {
            character.is_ascii_alphanumeric() || character == '-' || character == '_'
        })
    {
        return Err(invalid("artifact ID is malformed"));
    }
    Ok(())
}

fn validate_shared_file_id(value: &str) -> Result<(), AnyError> {
    if value.len() != 32 || !value.chars().all(|character| character.is_ascii_hexdigit() && !character.is_ascii_uppercase()) {
        return Err(invalid("shared file ID is malformed"));
    }
    Ok(())
}

fn validate_shared_file_name(value: &str) -> Result<(), AnyError> {
    if value.is_empty()
        || value.as_bytes().len() > 255
        || value.chars().any(|character| character.is_control() || character == '/' || character == '\\')
    {
        return Err(invalid("shared file name is malformed"));
    }
    Ok(())
}

fn validate_mime(value: &str) -> Result<(), AnyError> {
    if value.is_empty()
        || value.len() > 127
        || !value.is_ascii()
        || value.chars().any(|character| character.is_control() || character.is_whitespace())
        || !value.contains('/')
    {
        return Err(invalid("shared file MIME type is malformed"));
    }
    Ok(())
}

fn hex_lower(value: &[u8]) -> String {
    value.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn invalid(message: &str) -> AnyError {
    io::Error::new(io::ErrorKind::InvalidInput, message).into()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::Cell;

    #[test]
    fn parses_openssl_fingerprint() {
        let value = "C7:6E:BF:95:B7:7A:33:76:5A:BD:23:B0:4B:30:C4:84:E8:01:E4:C3:BD:CB:81:83:6B:8F:07:46:92:3A:63:74";
        assert_eq!(format_pin(&parse_pin(value).unwrap()), value);
    }

    #[test]
    fn rejects_short_pin() {
        assert!(parse_pin("cafe").is_err());
    }

    #[test]
    fn validates_shared_file_identifiers_and_headers() {
        assert!(validate_shared_file_id("0123456789abcdef0123456789abcdef").is_ok());
        assert!(validate_shared_file_id("../../etc/passwd").is_err());
        assert!(validate_shared_file_name("screenshot.png").is_ok());
        assert!(validate_shared_file_name("../screenshot.png").is_err());
        assert!(validate_mime("image/png").is_ok());
        assert!(validate_mime("image/png\r\nx-evil: yes").is_err());
    }

    #[test]
    fn cleanup_runs_without_changing_request_result() {
        let cleaned = Cell::new(false);
        let success = finish_with_cleanup(Ok::<_, AnyError>("response"), || cleaned.set(true));
        assert_eq!(success.unwrap(), "response");
        assert!(cleaned.get());

        cleaned.set(false);
        let failure = finish_with_cleanup::<()>(Err(invalid("request failed")), || {
            cleaned.set(true)
        });
        assert_eq!(failure.unwrap_err().to_string(), "request failed");
        assert!(cleaned.get());
    }
}
