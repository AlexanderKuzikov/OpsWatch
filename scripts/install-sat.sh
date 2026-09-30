#!/bin/bash
# OpsWatch installer for sat. Idempotent: safe to re-run.
set -euo pipefail

BIN=/usr/local/bin/opswatch
ROOT=/home/deploy/opswatch
CONFIG=$ROOT/config/registry.json
ENVFILE=/etc/opswatch.env

echo "==> $0"

if [ ! -x "$BIN" ]; then
  echo "!! $BIN not found. Build and scp it first:" >&2
  echo "   GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o opswatch ./cmd/opswatch" >&2
  echo "   scp opswatch deploy@135.106.192.125:/tmp/opswatch" >&2
  exit 1
fi

if [ ! -f "$CONFIG" ]; then
  echo "!! $CONFIG not found. Copy config/registry.json there first." >&2
  exit 1
fi

# SMTP password lives here, root-owned, never in the repo.
if [ ! -f "$ENVFILE" ]; then
  echo "==> creating $ENVFILE (fill in the Gmail app password)"
  sudo tee "$ENVFILE" > /dev/null <<'EOF'
# Gmail app password for alex.kuzikov@gmail.com.
# Generate at https://myaccount.google.com/apppasswords (needs 2FA on).
# It is a 16-character code, spaces are fine.
OPSWATCH_SMTP_PASS=
EOF
  sudo chown root:root "$ENVFILE"
  sudo chmod 0600 "$ENVFILE"
  echo "    -> edit with: sudo nano $ENVFILE   (until then, mail is skipped)"
fi

echo "==> units"
sudo tee /etc/systemd/system/opswatch-report.service > /dev/null <<EOF
[Unit]
Description=OpsWatch report — payments, server, certificates, quotas
After=network-online.target

[Service]
Type=oneshot
User=deploy
EnvironmentFile=$ENVFILE
Environment=OPSWATCH_REGISTRY=$CONFIG
ExecStart=$BIN report -registry $CONFIG -mail problems -out $ROOT/last-report.md -quiet
# A failing check must not be retried by systemd; the next timer run covers it.
SuccessExitStatus=0 1
EOF

sudo tee /etc/systemd/system/opswatch-report.timer > /dev/null <<'EOF'
[Unit]
Description=Run OpsWatch report every 15 minutes

[Timer]
OnCalendar=*:0/15
Persistent=true
AccuracySec=1m

[Install]
WantedBy=timers.target
EOF

sudo tee /etc/systemd/system/opswatch-heartbeat.service > /dev/null <<EOF
[Unit]
Description=OpsWatch heartbeat — daily "still alive" mail
After=network-online.target

[Service]
Type=oneshot
User=deploy
EnvironmentFile=$ENVFILE
Environment=OPSWATCH_REGISTRY=$CONFIG
ExecStart=$BIN heartbeat -registry $CONFIG
EOF

sudo tee /etc/systemd/system/opswatch-heartbeat.timer > /dev/null <<'EOF'
[Unit]
Description=Daily OpsWatch heartbeat. No mail means the host is gone —
a self-hosted check can never report its own death.

[Timer]
OnCalendar=daily
Persistent=true
AccuracySec=5m

[Install]
WantedBy=timers.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now opswatch-report.timer opswatch-heartbeat.timer

echo "==> validation"
$BIN validate -registry "$CONFIG"

echo "==> first run"
sudo systemctl start opswatch-report.service
systemctl is-active opswatch-report.timer opswatch-heartbeat.timer
echo
sudo journalctl -t opswatch -n 10 --no-pager 2>/dev/null || true
sudo journalctl -u opswatch-report -n 10 --no-pager || true
echo
echo "done. report:  sudo journalctl -u opswatch-report -n 50 --no-pager"
echo "       file:  $ROOT/last-report.md"