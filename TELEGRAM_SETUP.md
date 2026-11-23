# Telegram control path

Phone mode is only useful if you still have a way to talk to the machine after
the desktop is gone. On this setup that path is Telegram:

```
phone (Telegram app)
  -> Telegram Bot API
  -> opencode-telegram.service   (the bot)
  -> opencode-serve.service      (OpenCode V2 background server)
  -> OpenCode sessions and agents
```

Both services are systemd **user** units. `opencode-phone-mode` lists them in
`keep_services`, enables linger, and leaves Wi-Fi unblocked, so they keep
running after `isolate_target` shuts down `graphical.target`.

## What is installed here

| Piece | Location |
|---|---|
| Bot CLI | `opencode-telegram` from `@grinev/opencode-telegram-bot` (Node 22.14+) |
| Bot config | `~/.config/opencode-telegram-bot/.env` (mode 600) and `settings.json` |
| Bot unit | `~/.config/systemd/user/opencode-telegram.service` |
| Server unit | `~/.config/systemd/user/opencode-serve.service` |
| OpenCode server password | `~/.config/opencode/service.json` |

Use `systemctl --user` to check the bot. `opencode-telegram status` only knows
about the bot's own `--daemon` mode and will say "stopped" when systemd is
supervising it.

## Set it up on a fresh machine

### 1. Get the credentials

1. Open [@BotFather](https://t.me/BotFather), send `/newbot`, and copy the bot
   token.
2. Send a message to [@userinfobot](https://t.me/userinfobot) and copy your
   numeric Telegram user ID. The bot whitelists this ID and ignores everyone
   else.
3. Install Node.js 22.14+ and OpenCode V2, then install the bot:
   ```sh
   npm install -g @grinev/opencode-telegram-bot
   ```

### 2. Configure the bot

Run `opencode-telegram start` once without systemd. The wizard asks for the
interface language, bot token, user ID, OpenCode version, API URL, and server
credentials, then writes `~/.config/opencode-telegram-bot/.env`.

For OpenCode V2 in background service mode, the important values are:

```dotenv
TELEGRAM_BOT_TOKEN=<token from BotFather>
TELEGRAM_ALLOWED_USER_ID=<your numeric user ID>
OPENCODE_SERVER_VERSION=v2
OPENCODE_API_URL=http://127.0.0.1:49374
OPENCODE_SERVER_USERNAME=opencode
OPENCODE_SERVER_PASSWORD=<output of: opencode service get password>
OPENCODE_MODEL_PROVIDER=opencode
OPENCODE_MODEL_ID=big-pickle
```

Keep this file mode 600 and never commit it. You can re-run the wizard later
with `opencode-telegram config`.

### 3. Verify in the foreground

```sh
opencode-telegram start
```

Send a message to your bot in Telegram. If it replies, stop it with Ctrl-C and
move on to systemd. Do **not** use `opencode-telegram start --daemon` when a
service manager is supervising it.

### 4. Add the systemd user units

Create `~/.config/systemd/user/opencode-serve.service`:

```ini
[Unit]
Description=OpenCode background server (headless)
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/opencode service start
ExecStop=/usr/bin/opencode service stop
TimeoutStartSec=60

[Install]
WantedBy=default.target
```

Create `~/.config/systemd/user/opencode-telegram.service`, using the absolute
path from `command -v opencode-telegram` in `ExecStart`:

```ini
[Unit]
Description=OpenCode Telegram bot
Wants=opencode-serve.service
After=network-online.target opencode-serve.service

[Service]
Type=simple
ExecStart=/path/to/opencode-telegram start
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
```

Then enable them and let them run without a login session:

```sh
systemctl --user daemon-reload
loginctl enable-linger "$USER"
systemctl --user enable --now opencode-serve.service opencode-telegram.service
systemctl --user status opencode-telegram.service
journalctl --user -u opencode-telegram.service -f
```

### 5. Wire it into phone mode

In `~/.config/opencode-phone-mode/config.toml`:

```toml
keep_services = ["opencode-serve.service", "opencode-telegram.service"]
keep_radios_on = ["wlan"]
```

`keep_services` is what keeps both units alive through the target switch. The
tool leaves already-active units running and starts inactive ones; it never
restarts an active unit, so calling `on` from a session hosted by one of them
cannot kill itself. `keep_radios_on` keeps Wi-Fi up so the bot can reach
`api.telegram.org`.

Your `~/.config/opencode/AGENTS.md` battery-mode rule checks the
`~/.cache/opencode-phone-mode/active` marker, so agents are asked to stay light
while the desktop is gone.

## Troubleshooting

- **`opencode-telegram status` says stopped.** Expected under systemd. Use
  `systemctl --user status opencode-telegram.service`.
- **`sendMessage` failed / `HttpError: Network request failed`.** The bot could
  not reach Telegram. Check Wi-Fi (`opencode-phone-mode status` shows radios)
  and `curl https://api.telegram.org`. If IPv6 DNS works but outbound IPv6 does
  not, set `TELEGRAM_FORCE_IPV4=true`. If you need a proxy, set
  `TELEGRAM_PROXY_URL`. Watch `journalctl --user -u opencode-telegram.service -f`.
- **Bot ignores your messages.** Check `TELEGRAM_ALLOWED_USER_ID`; unauthorized
  users are silently ignored.
- **Server errors in the log.** Check `OPENCODE_SERVER_VERSION` matches the
  installed CLI and that `OPENCODE_SERVER_PASSWORD` matches
  `opencode service get password`. V2 requires a password.
- **Services die when the lid closes or the desktop stops.** Confirm linger is
  on (`loginctl show-user "$USER" -p Linger`) and both units are in
  `keep_services`. `opencode-phone-mode status` reports services and linger.
