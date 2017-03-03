// telegram bot for using systemctl remotely
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/profile"

	svc "github.com/meinside/rpi-tools/service"
	bot "github.com/meinside/telegram-bot-go"
)

const (
	ConfigFilename = "config.json"

	//DoProfiling = true
	DoProfiling = false

	GithubPageUrl = "https://github.com/meinside/telegram-bot-systemd"

	// for monitoring
	DefaultMonitorIntervalSeconds = 3

	// commands
	CommandStart  = "/start"
	CommandStatus = "/status"
	CommandHelp   = "/help"
	CommandCancel = "/cancel"

	// commands for systemctl
	CommandServiceStatus = "/servicestatus"
	CommandServiceStart  = "/servicestart"
	CommandServiceStop   = "/servicestop"

	// messages
	MessageDefault                = "Input your command:"
	MessageUnknownCommand         = "Unknown command."
	MessageNoControllableServices = "No controllable services."
	MessageServiceToStart         = "Select service to start:"
	MessageServiceToStop          = "Select service to stop:"
	MessageCancel                 = "Cancel"
	MessageCanceled               = "Canceled."
)

type Status int16

const (
	StatusWaiting Status = iota
)

type Session struct {
	UserId        string
	CurrentStatus Status
}

type SessionPool struct {
	Sessions map[string]Session
	sync.Mutex
}

// struct for config file
type Config struct {
	ApiToken             string   `json:"api_token"`
	AvailableIds         []string `json:"available_ids"`
	ControllableServices []string `json:"controllable_services"`
	MonitorInterval      int      `json:"monitor_interval"`
	IsVerbose            bool     `json:"is_verbose"`
}

// Read config
func getConfig() (config Config, err error) {
	_, filename, _, _ := runtime.Caller(0) // = __FILE__

	if file, err := ioutil.ReadFile(filepath.Join(path.Dir(filename), ConfigFilename)); err == nil {
		var conf Config
		if err := json.Unmarshal(file, &conf); err == nil {
			return conf, nil
		} else {
			return Config{}, err
		}
	} else {
		return Config{}, err
	}
}

// get uptime of this bot in seconds
func getUptime(launched time.Time) (uptime string) {
	now := time.Now()
	gap := now.Sub(launched)

	uptimeSeconds := int(gap.Seconds())
	numDays := uptimeSeconds / (60 * 60 * 24)
	numHours := (uptimeSeconds % (60 * 60 * 24)) / (60 * 60)

	return fmt.Sprintf("*%d* day(s) *%d* hour(s)", numDays, numHours)
}

// get memory usage
func getMemoryUsage() (usage string) {
	m := new(runtime.MemStats)
	runtime.ReadMemStats(m)

	return fmt.Sprintf("Sys: *%.1f MB*, Heap: *%.1f MB*", float32(m.Sys)/1024/1024, float32(m.HeapAlloc)/1024/1024)
}

// variables
var apiToken string
var monitorInterval int
var isVerbose bool
var availableIds []string
var controllableServices []string
var pool SessionPool
var cliPort int
var launched time.Time

// keyboards
var allKeyboards = [][]bot.KeyboardButton{
	bot.NewKeyboardButtons(CommandServiceStatus, CommandServiceStart, CommandServiceStop),
	bot.NewKeyboardButtons(CommandStatus, CommandHelp),
}

// initialization
func init() {
	launched = time.Now()

	// for profiling
	if DoProfiling {
		defer profile.Start(
			profile.BlockProfile,
			profile.CPUProfile,
			profile.MemProfile,
		).Stop()
	}

	// read variables from config file
	if config, err := getConfig(); err == nil {
		apiToken = config.ApiToken
		availableIds = config.AvailableIds
		controllableServices = config.ControllableServices
		monitorInterval = config.MonitorInterval
		if monitorInterval <= 0 {
			monitorInterval = DefaultMonitorIntervalSeconds
		}
		isVerbose = config.IsVerbose

		// initialize variables
		sessions := make(map[string]Session)
		for _, v := range availableIds {
			sessions[v] = Session{
				UserId:        v,
				CurrentStatus: StatusWaiting,
			}
		}
		pool = SessionPool{
			Sessions: sessions,
		}
	} else {
		panic(err)
	}
}

// check if given Telegram id is available
func isAvailableId(id string) bool {
	for _, v := range availableIds {
		if v == id {
			return true
		}
	}
	return false
}

// check if given service is controllable
func isControllableService(service string) bool {
	for _, v := range controllableServices {
		if v == service {
			return true
		}
	}
	return false
}

// for showing help message
func getHelp() string {
	return fmt.Sprintf(`
Following commands are supported:

*For Systemctl*

%s : show status of each service (systemctl is-active)
%s : start a service (systemctl start)
%s : stop a service (systemctl stop)

*Others*

%s : show this bot's status
%s : show this help message
`,
		CommandServiceStatus,
		CommandServiceStart,
		CommandServiceStop,

		CommandStatus,
		CommandHelp,
	)
}

// for showing current status of this bot
func getStatus() string {
	return fmt.Sprintf("Uptime: %s\nMemory Usage: %s", getUptime(launched), getMemoryUsage())
}

// parse service command and start/stop given service
func parseServiceCommand(txt string) (message string, keyboards [][]bot.InlineKeyboardButton) {
	message = MessageNoControllableServices

	for _, cmd := range []string{CommandServiceStart, CommandServiceStop} {
		if strings.HasPrefix(txt, cmd) {
			service := strings.TrimSpace(strings.Replace(txt, cmd, "", 1))

			if isControllableService(service) {
				if strings.HasPrefix(txt, CommandServiceStart) { // start service
					if output, ok := svc.SystemctlStart(service); ok {
						message = fmt.Sprintf("Started service: %s", service)
					} else {
						message = output
					}
				} else if strings.HasPrefix(txt, CommandServiceStop) { // stop service
					if output, ok := svc.SystemctlStop(service); ok {
						message = fmt.Sprintf("Stopped service: %s", service)
					} else {
						message = output
					}
				} else if strings.HasPrefix(txt, CommandCancel) { // cancel command
					message = MessageCanceled
				}
			} else {
				if strings.HasPrefix(txt, CommandServiceStart) { // start service
					message = MessageServiceToStart
				} else if strings.HasPrefix(txt, CommandServiceStop) { // stop service
					message = MessageServiceToStop
				}

				keys := map[string]string{}
				for _, v := range controllableServices {
					keys[v] = fmt.Sprintf("%s %s", cmd, v)
				}

				keyboards = bot.NewInlineKeyboardButtonsAsRowsWithCallbackData(keys)
				keyboards = append(keyboards, // cancel button
					[]bot.InlineKeyboardButton{
						bot.InlineKeyboardButton{
							Text:         MessageCancel,
							CallbackData: CommandCancel,
						},
					},
				)
			}
		}
		continue
	}

	return message, keyboards
}

// process incoming update from Telegram
func processUpdate(b *bot.Bot, update bot.Update) bool {
	// check username
	var userId string
	if update.Message.From.Username == nil {
		log.Printf("*** Not allowed (no user name): %s\n", *update.Message.From.FirstName)
		return false
	}
	userId = *update.Message.From.Username
	if !isAvailableId(userId) {
		log.Printf("*** Id not allowed: %s\n", userId)

		return false
	}

	// process result
	result := false

	pool.Lock()
	if session, exists := pool.Sessions[userId]; exists {
		// text from message
		var txt string
		if update.Message.HasText() {
			txt = *update.Message.Text
		} else {
			txt = ""
		}

		var message string
		var options map[string]interface{} = map[string]interface{}{
			"reply_markup": bot.ReplyKeyboardMarkup{
				Keyboard:       allKeyboards,
				ResizeKeyboard: true,
			},
			"parse_mode": bot.ParseModeMarkdown,
		}

		switch session.CurrentStatus {
		case StatusWaiting:
			switch {
			// start
			case strings.HasPrefix(txt, CommandStart):
				message = MessageDefault
			// systemctl
			case strings.HasPrefix(txt, CommandServiceStatus):
				statuses, _ := svc.SystemctlStatus(controllableServices)
				for service, status := range statuses {
					message += fmt.Sprintf("%s: *%s*\n", service, status)
				}
			case strings.HasPrefix(txt, CommandServiceStart) || strings.HasPrefix(txt, CommandServiceStop):
				if len(controllableServices) > 0 {
					var keyboards [][]bot.InlineKeyboardButton
					message, keyboards = parseServiceCommand(txt)

					if keyboards != nil {
						options["reply_markup"] = bot.InlineKeyboardMarkup{
							InlineKeyboard: keyboards,
						}
					}
				} else {
					message = MessageNoControllableServices
				}
			case strings.HasPrefix(txt, CommandStatus):
				message = getStatus()
			case strings.HasPrefix(txt, CommandHelp):
				message = getHelp()
				options["reply_markup"] = bot.InlineKeyboardMarkup{ // inline keyboard for link to github page
					InlineKeyboard: [][]bot.InlineKeyboardButton{
						bot.NewInlineKeyboardButtonsWithUrl(map[string]string{
							"GitHub": GithubPageUrl,
						}),
					},
				}
			// fallback
			default:
				message = fmt.Sprintf("*%s*: %s", txt, MessageUnknownCommand)
			}
		}

		// send message
		if sent := b.SendMessage(update.Message.Chat.Id, &message, options); sent.Ok {
			result = true
		} else {
			log.Printf("*** Failed to send message: %s\n", *sent.Description)
		}
	} else {
		log.Printf("*** Session does not exist for id: %s\n", userId)
	}
	pool.Unlock()

	return result
}

// process incoming callback query
func processCallbackQuery(b *bot.Bot, update bot.Update) bool {
	query := *update.CallbackQuery
	txt := *query.Data

	// process result
	result := false

	var message string = ""
	if strings.HasPrefix(txt, CommandCancel) { // cancel command
		message = ""
	} else if strings.HasPrefix(txt, CommandServiceStart) || strings.HasPrefix(txt, CommandServiceStop) { // service
		message, _ = parseServiceCommand(txt)
	} else {
		log.Printf("*** Unprocessable callback query: %s\n", txt)

		return result // == false
	}

	// answer callback query
	options := map[string]interface{}{}
	if len(message) > 0 {
		options["text"] = message
	}
	if apiResult := b.AnswerCallbackQuery(query.Id, options); apiResult.Ok {
		// edit message and remove inline keyboards
		options := map[string]interface{}{
			"chat_id":    query.Message.Chat.Id,
			"message_id": query.Message.MessageId,
		}

		if len(message) <= 0 {
			message = MessageCanceled
		}
		if apiResult := b.EditMessageText(&message, options); apiResult.Ok {
			result = true
		} else {
			log.Printf("*** Failed to edit message text: %s\n", *apiResult.Description)
		}
	} else {
		log.Printf("*** Failed to answer callback query: %+v\n", query)
	}

	return result
}

func main() {
	// catch SIGINT and SIGTERM and terminate gracefully
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		os.Exit(1)
	}()

	client := bot.NewClient(apiToken)
	client.Verbose = isVerbose

	// get info about this bot
	if me := client.GetMe(); me.Ok {
		log.Printf("Launching bot: @%s (%s)\n", *me.Result.Username, *me.Result.FirstName)

		// delete webhook (getting updates will not work when wehbook is set up)
		if unhooked := client.DeleteWebhook(); unhooked.Ok {
			// wait for new updates
			client.StartMonitoringUpdates(0, monitorInterval, func(b *bot.Bot, update bot.Update, err error) {
				if err == nil {
					if update.HasMessage() {
						// process message
						processUpdate(b, update)
					} else if update.HasCallbackQuery() {
						// process callback query
						processCallbackQuery(b, update)
					}
				} else {
					log.Printf("*** Error while receiving update (%s)\n", err)
				}
			})
		} else {
			panic("Failed to delete webhook")
		}
	} else {
		panic("Failed to get info of the bot")
	}
}
