#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
    echo "Run this script as the target login user, not as root." >&2
    exit 1
fi

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
target_user=$(id -un)
target_group=$(id -gn)
config_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/migi
state_dir=${XDG_STATE_HOME:-"$HOME/.local/state"}/migi
binary_dir="$HOME/.local/libexec/migi"
unit_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/systemd/user
binary_tmp="$binary_dir/migi-server.new"

install -d -m 0700 "$config_dir" "$state_dir" "$binary_dir"
install -d -m 0755 "$unit_dir"

(
    cd "$project_dir/server"
    go build -o "$binary_tmp" ./cmd/migi-server
)
chmod 0755 "$binary_tmp"
mv -f "$binary_tmp" "$binary_dir/migi-server"
install -m 0644 "$project_dir/deploy/systemd/migi-user.service" "$unit_dir/migi.service"
if [ ! -e "$config_dir/migi.env" ]; then
    install -m 0600 "$project_dir/deploy/systemd/migi-user.env.example" "$config_dir/migi.env"
fi

echo "Stopping the system service and copying its state (sudo required once)..."
system_stopped=0
sudo systemctl stop migi.service
system_stopped=1

rollback() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$system_stopped" -eq 0 ]; then
		exit "$status"
	fi
    echo "User service failed; restoring the system service." >&2
    systemctl --user disable --now migi.service >/dev/null 2>&1 || true
    sudo systemctl enable --now migi.service
	exit "$status"
}
trap rollback EXIT HUP INT TERM

sudo install -o "$target_user" -g "$target_group" -m 0600 \
    /var/lib/migi/migi.db "$state_dir/migi.db"
sudo install -o "$target_user" -g "$target_group" -m 0644 \
    /etc/migi/server.crt "$config_dir/server.crt"
sudo install -o "$target_user" -g "$target_group" -m 0600 \
    /etc/migi/server.key "$config_dir/server.key"

systemctl --user daemon-reload
if ! systemctl --user enable --now migi.service; then
	exit 1
fi

# A unit may be briefly reported active before an exec-time sandbox failure is
# observed. Require it to survive startup and answer its local health endpoint.
ready=0
attempt=0
while [ "$attempt" -lt 20 ]; do
	if systemctl --user is-active --quiet migi.service && \
		curl --max-time 1 --fail --silent http://127.0.0.1:8787/healthz >/dev/null; then
		ready=1
		break
	fi
	attempt=$((attempt + 1))
	sleep 0.25
done
if [ "$ready" -ne 1 ]; then
	echo "User service did not become healthy." >&2
	exit 1
fi

sudo systemctl disable migi.service
system_stopped=0
trap - EXIT HUP INT TERM

echo "Migi now runs as $target_user under the user systemd manager."
echo "The previous system unit and source data were retained for rollback."
echo "Use: systemctl --user status migi.service"
