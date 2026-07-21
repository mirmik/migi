use jni::JNIEnv;
use jni::objects::{JClass, JObject, JString, JValue};
use jni::sys::{jlong, jstring};
use quiche::h3::NameValue;
use rand::RngCore;
use sha2::{Digest, Sha256};
use std::error::Error;
use std::io;
use std::net::{SocketAddr, ToSocketAddrs, UdpSocket};
use std::time::Duration;
use url::Url;

const MAX_DATAGRAM_SIZE: usize = 1350;
const CALLBACK_POLL: Duration = Duration::from_millis(100);

type AnyError = Box<dyn Error + Send + Sync>;

#[unsafe(no_mangle)]
pub extern "system" fn Java_dev_migi_app_NativeQuicClient_run(
    mut env: JNIEnv,
    _class: JClass,
    endpoint: JString,
    after: jlong,
    device_id: JString,
    certificate_pin: JString,
    callback: JObject,
) -> jstring {
    let result = (|| -> Result<(), AnyError> {
        let endpoint: String = env.get_string(&endpoint)?.into();
        let device_id: String = env.get_string(&device_id)?.into();
        let certificate_pin: String = env.get_string(&certificate_pin)?.into();
        let expected_pin = parse_pin(&certificate_pin)?;
        run_client(
            &mut env,
            &callback,
            &endpoint,
            after.max(0) as u64,
            &device_id,
            &expected_pin,
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

fn run_client(
    env: &mut JNIEnv,
    callback: &JObject,
    endpoint: &str,
    after: u64,
    device_id: &str,
    expected_pin: &[u8; 32],
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

    let mut config = quiche::Config::new(quiche::PROTOCOL_VERSION)?;
    // The exact leaf certificate is authenticated immediately after the TLS
    // handshake, before any HTTP request or application data is sent.
    config.verify_peer(false);
    config.set_application_protos(quiche::h3::APPLICATION_PROTOCOL)?;
    config.set_max_idle_timeout(90_000);
    config.set_max_recv_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_max_send_udp_payload_size(MAX_DATAGRAM_SIZE);
    config.set_initial_max_data(10_000_000);
    config.set_initial_max_stream_data_bidi_local(1_000_000);
    config.set_initial_max_stream_data_bidi_remote(1_000_000);
    config.set_initial_max_stream_data_uni(1_000_000);
    config.set_initial_max_streams_bidi(100);
    config.set_initial_max_streams_uni(100);

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
            let path = format!("/v1/events?after={after}");
            let headers = request_headers("GET", &url, &path, None);
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
                send_ack(http3, &mut connection, &url, device_id, through)?;
            }
        }
    }
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

fn request_headers<'a>(
    method: &'a str,
    url: &'a Url,
    path: &'a str,
    content_length: Option<usize>,
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
    headers
}

fn send_ack(
    http3: &mut quiche::h3::Connection,
    connection: &mut quiche::Connection,
    url: &Url,
    device_id: &str,
    through: i64,
) -> Result<(), AnyError> {
    let body = format!(r#"{{"device_id":"{device_id}","through":{through}}}"#);
    let headers = request_headers("POST", url, "/v1/ack", Some(body.len()));
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

fn invalid(message: &str) -> AnyError {
    io::Error::new(io::ErrorKind::InvalidInput, message).into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_openssl_fingerprint() {
        let value = "C7:6E:BF:95:B7:7A:33:76:5A:BD:23:B0:4B:30:C4:84:E8:01:E4:C3:BD:CB:81:83:6B:8F:07:46:92:3A:63:74";
        assert_eq!(format_pin(&parse_pin(value).unwrap()), value);
    }

    #[test]
    fn rejects_short_pin() {
        assert!(parse_pin("cafe").is_err());
    }
}
