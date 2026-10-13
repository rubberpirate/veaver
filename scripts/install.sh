#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
install_root=${DESTDIR:-}
veaver_binary=${VEAVER_BINARY:-$project_dir/build/veaver}

if [ -z "$install_root" ] && [ "$(id -u)" -ne 0 ]; then
  echo 'Run sudo ./scripts/install.sh after building, or set DESTDIR to stage a package.' >&2
  exit 1
fi
if [ ! -f "$veaver_binary" ]; then
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
install -d -m 0700 "$install_root/etc/veaver"
install -m 0755 "$veaver_binary" "$install_root/usr/local/bin/veaver"
install -m 0644 "$project_dir/deploy/veaver.service" "$install_root/etc/systemd/system/veaver.service"
install -m 0755 "$project_dir/deploy/90-veaver" "$install_root/etc/NetworkManager/dispatcher.d/90-veaver"
if [ ! -e "$install_root/etc/veaver/config.json" ]; then
  install -m 0600 "$project_dir/deploy/config.json" "$install_root/etc/veaver/config.json"
fi
if [ -z "$install_root" ]; then
  systemctl daemon-reload
  systemctl enable veaver.service
  systemctl restart veaver.service
  echo 'Installed in Observe mode. Physical connections and routing remain unchanged.'
  echo 'Open the terminal console with: sudo veaver tui'
else
  echo "Package staged in $install_root"
fi
