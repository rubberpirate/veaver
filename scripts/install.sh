#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
install_root=${DESTDIR:-}
linkweave_binary=${LINKWEAVE_BINARY:-$project_dir/build/linkweave}

if [ -z "$install_root" ] && [ "$(id -u)" -ne 0 ]; then
  echo 'Run sudo ./scripts/install.sh after building, or set DESTDIR to stage a package.' >&2
  exit 1
fi
if [ ! -f "$linkweave_binary" ]; then
  echo 'Build first: ./scripts/build.sh' >&2
  exit 1
fi
if [ -z "$install_root" ]; then
  for tool in nmcli ip nft journalctl systemctl timeout; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      echo "Missing $tool. Install NetworkManager, iproute2, nftables and systemd with your distribution's package manager." >&2
      exit 1
    fi
  done
  if ! systemctl is-active --quiet NetworkManager; then
    echo 'NetworkManager must be running before installation.' >&2
    exit 1
  fi
fi
install -d -m 0755 "$install_root/usr/local/bin" "$install_root/etc/NetworkManager/dispatcher.d" "$install_root/etc/systemd/system"
install -d -m 0700 "$install_root/etc/linkweave"
install -m 0755 "$linkweave_binary" "$install_root/usr/local/bin/linkweave"
install -m 0644 "$project_dir/deploy/linkweave.service" "$install_root/etc/systemd/system/linkweave.service"
install -m 0755 "$project_dir/deploy/90-linkweave" "$install_root/etc/NetworkManager/dispatcher.d/90-linkweave"
if [ ! -e "$install_root/etc/linkweave/config.json" ]; then
  install -m 0600 "$project_dir/deploy/config.json" "$install_root/etc/linkweave/config.json"
fi
if [ -z "$install_root" ]; then
  systemctl daemon-reload
  systemctl enable linkweave.service
  systemctl restart linkweave.service
  echo 'Installed in Observe mode. Physical connections and routing remain unchanged.'
  echo 'Open the URL printed by: sudo linkweave dashboard'
else
  echo "Package staged in $install_root"
fi
