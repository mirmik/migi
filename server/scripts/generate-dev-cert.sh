#!/usr/bin/env sh
set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "usage: $0 OUTPUT_DIR SERVER_IP [SERVER_DNS]" >&2
    exit 2
fi

output_dir=$1
server_ip=$2
server_dns=${3:-migi.local}

mkdir -p "$output_dir"
for name in server.key server.crt; do
    if [ -e "$output_dir/$name" ]; then
        echo "refusing to overwrite $output_dir/$name" >&2
        exit 1
    fi
done

umask 077
openssl genpkey \
    -algorithm EC \
    -pkeyopt ec_paramgen_curve:P-256 \
    -out "$output_dir/server.key"
openssl req \
    -x509 \
    -new \
    -sha256 \
    -days 3650 \
    -key "$output_dir/server.key" \
    -subj "/CN=$server_dns" \
    -addext "subjectAltName=DNS:$server_dns,IP:$server_ip" \
    -addext "basicConstraints=critical,CA:FALSE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment" \
    -addext "extendedKeyUsage=serverAuth" \
    -out "$output_dir/server.crt"

echo "server certificate: $output_dir/server.crt"
echo "server private key: $output_dir/server.key"
openssl x509 -in "$output_dir/server.crt" -noout -fingerprint -sha256
