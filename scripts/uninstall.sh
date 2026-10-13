#!/bin/sh
set -eu
if [ "$(id -u)" -ne 0 ]; then echo 'Run with sudo.' >&2; exit 1; fi
if systemctl is-active --quiet linkweave.service; then
  /usr/local/bin/linkweave restore --state-dir /var/lib/linkweave
fi
systemctl disable --now linkweave.service
/usr/local/bin/linkweave cleanup --state-dir /var/lib/linkweave
rm -f /etc/systemd/system/linkweave.service /etc/NetworkManager/dispatcher.d/90-linkweave /usr/local/bin/linkweave
systemctl daemon-reload
echo 'Removed Linkweave service. Configuration and diagnostics retained in /etc/linkweave and /var/lib/linkweave.'
