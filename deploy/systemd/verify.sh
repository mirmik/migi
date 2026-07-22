#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
unit_file="$script_dir/migi.service"
env_file="$script_dir/migi.env.example"
journal_file="$script_dir/journald@migi.conf.d/retention.conf"
temporary_dir=$(mktemp -d)
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

require_line() {
    expected=$1
    file=$2
    if ! grep -Fqx "$expected" "$file"; then
        echo "missing required line in $file: $expected" >&2
        exit 1
    fi
}

for required in \
    'User=migi' \
    'Group=migi' \
    'UMask=0077' \
    'Restart=on-failure' \
    'NoNewPrivileges=yes' \
    'ProtectSystem=strict' \
    'ProtectHome=yes' \
    'MemoryMax=256M' \
    'TasksMax=64' \
    'LoadCredential=server.key:/etc/migi/server.key' \
    'LogNamespace=migi'
do
    require_line "$required" "$unit_file"
done

require_line 'MIGI_INGEST_LISTEN=127.0.0.1:8787' "$env_file"
require_line 'MIGI_ADMIN_LISTEN=127.0.0.1:8788' "$env_file"
require_line 'SystemMaxUse=256M' "$journal_file"
require_line 'MaxRetentionSec=14day' "$journal_file"

if grep -Eiq '(secret|token|private[_-]?key)=' "$env_file"; then
    echo "the environment template must not contain credentials" >&2
    exit 1
fi

# systemd-analyze validates a temporary copy with a known local executable.
# Production paths and arguments remain otherwise unchanged.
sed 's#^ExecStart=/usr/local/libexec/migi/migi-server#ExecStart=/bin/true#' \
    "$unit_file" > "$temporary_dir/migi.service"

SYSTEMD_UNIT_PATH="$temporary_dir:/usr/lib/systemd/system:/lib/systemd/system" \
    systemd-analyze verify --man=no "$temporary_dir/migi.service"
# systemd expresses the 3.0 exposure threshold in tenths here.
systemd-analyze security \
    --offline=yes \
    --no-pager \
    --threshold=30 \
    "$temporary_dir/migi.service" >/dev/null

echo "systemd deployment kit verification passed"
