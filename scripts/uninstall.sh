#!/bin/sh
set -eu
if [ "$(id -u)" -ne 0 ]; then echo 'Run with sudo.' >&2; exit 1; fi
if systemctl is-active --quiet veaver.service; then
  /usr/local/bin/veaver restore --state-dir /var/lib/veaver
fi
systemctl disable --now veaver.service
/usr/local/bin/veaver cleanup --state-dir /var/lib/veaver
rm -f /etc/systemd/system/veaver.service /etc/NetworkManager/dispatcher.d/90-veaver /usr/local/bin/veaver
systemctl daemon-reload
echo 'Removed Veaver service. Configuration and diagnostics retained in /etc/veaver and /var/lib/veaver.'
