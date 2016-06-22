# Telegram Bot for Systemd

With this bot, you can systemctl is-active/start/stop services remotely.

## 0. Prepare

Install Go and generate your Telegram bot's API token.

## 1. Install

```bash
$ go get -u github.com/meinside/telegram-bot-systemd
$ cd $GOPOATH/src/github.com/meinside/telegram-bot-systemd
$ cp config.json.sample config.json
$ vi config.json
```

and edit values to yours:

```json
{
	"api_token": "0123456789:abcdefghijklmnopqrstuvwyz-x-0a1b2c3d4e",
	"available_ids": [
		"your_telegram_id",
		"other_whitelisted_id"
	],
	"controllable_services": [
		"vpnserver",
		"minecraft-server"
	],
	"monitor_interval": 1,
	"is_verbose": false
}
```

## 2. Build and run

```bash
$ go build
```

and run it:

```bash
$ ./telegram-bot-systemd
```

## 3. Run as a service

### a. systemd

```bash
$ sudo cp systemd/telegram-bot-systemd.service /lib/systemd/system/
$ sudo vi /lib/systemd/system/telegram-bot-systemd.service
```

and edit **User**, **Group**, **WorkingDirectory** and **ExecStart** values.

It will launch automatically on boot with:

```bash
$ sudo systemctl enable telegram-bot-systemd.service
```

and will start with:

```bash
$ sudo systemctl start telegram-bot-systemd.service
```

## 998. Trouble shooting

TODO

## 999. License

MIT

