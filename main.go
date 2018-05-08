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
	configFilename = "config.json"

	//doProfiling = true
	doProfiling = false

	githubPageUrl = "https://github.com/meinside/telegram-bot-systemd"

	// for monitoring
	defaultMonitorIntervalSeconds = 3

	// commands
	commandStart  = "/start"
	commandStatus = "/status"
	commandHelp   = "/help"
	commandCancel = "/cancel"

	// commands for systemctl
	commandServiceStatus = "/servicestatus"
	commandServiceStart  = "/servicestart"
	commandServiceStop   = "/servicestop"

	// messages
	messageDefault                = "Input your command:"
	messageUnknownCommand         = "Unknown command."
	messageNoControllableServices = "No controllable services."
	messageServiceToStart         = "Select service to start:"
	messageServiceToStop          = "Select service to stop:"
	messageCancel                 = "Cancel"
	messageCanceled               = "Canceled."
)

type Status int16

const (
	StatusWaiting Status = iota
)

type Session struct {
	UserID        string
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

	if file, err := ioutil.ReadFile(filepath.Join(path.Dir(filename), configFilename)); err == nil {
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
	bot.NewKeyboardButtons(commandServiceStatus, commandServiceStart, commandServiceStop),
	bot.NewKeyboardButtons(commandStatus, commandHelp),
}

// initialization
func init() {
	launched = time.Now()

	// for profiling
	if doProfiling {
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
			monitorInterval = defaultMonitorIntervalSeconds
		}
		isVerbose = config.IsVerbose

		// initialize variables
		sessions := make(map[string]Session)
		for _, v := range availableIds {
			sessions[v] = Session{
				UserID:        v,
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
		commandServiceStatus,
		commandServiceStart,
		commandServiceStop,

		commandStatus,
		commandHelp,
	)
}

// for showing current status of this bot
func getStatus() string {
	return fmt.Sprintf("Uptime: %s\nMemory Usage: %s", getUptime(launched), getMemoryUsage())
}

// parse service command and start/stop given service
func parseServiceCommand(txt string) (message string, keyboards [][]bot.InlineKeyboardButton) {
	message = messageNoControllableServices

	for _, cmd := range []string{commandServiceStart, commandServiceStop} {
		if strings.HasPrefix(txt, cmd) {
			service := strings.TrimSpace(strings.Replace(txt, cmd, "", 1))

			if isControllableService(service) {
				if strings.HasPrefix(txt, commandServiceStart) { // start service
					if output, ok := svc.SystemctlStart(service); ok {
						message = fmt.Sprintf("Started service: %s", service)
					} else {
						message = output
					}
				} else if strings.HasPrefix(txt, commandServiceStop) { // stop service
					if output, ok := svc.SystemctlStop(service); ok {
						message = fmt.Sprintf("Stopped service: %s", service)
					} else {
						message = output
					}
				} else if strings.HasPrefix(txt, commandCancel) { // cancel command
					message = messageCanceled
				}
			} else {
				if strings.HasPrefix(txt, commandServiceStart) { // start service
					message = messageServiceToStart
				} else if strings.HasPrefix(txt, commandServiceStop) { // stop service
					message = messageServiceToStop
				}

				keys := map[string]string{}
				for _, v := range controllableServices {
					keys[v] = fmt.Sprintf("%s %s", cmd, v)
				}

				// keyboards
				keyboards = bot.NewInlineKeyboardButtonsAsRowsWithCallbackData(keys)

				// add a cancel button
				cancel := commandCancel
				keyboards = append(keyboards, []bot.InlineKeyboardButton{
					bot.InlineKeyboardButton{
						Text:         messageCancel,
						CallbackData: &cancel,
					}})
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
		log.Printf("*** Not allowed (no user name): %s", update.Message.From.FirstName)
		return false
	}
	userId = *update.Message.From.Username
	if !isAvailableId(userId) {
		log.Printf("*** Id not allowed: %s", userId)

		return false
	}

	// process result
	result := false

	pool.Lock()
	if session, exists := pool.Sessions[userId]; exists {
		// send chat action (typing...)
		b.SendChatAction(update.Message.Chat.ID, bot.ChatActionTyping)

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
			case strings.HasPrefix(txt, commandStart):
				message = messageDefault
			// systemctl
			case strings.HasPrefix(txt, commandServiceStatus):
				statuses, _ := svc.SystemctlStatus(controllableServices)
				for service, status := range statuses {
					message += fmt.Sprintf("%s: *%s*\n", service, status)
				}
			case strings.HasPrefix(txt, commandServiceStart) || strings.HasPrefix(txt, commandServiceStop):
				if len(controllableServices) > 0 {
					var keyboards [][]bot.InlineKeyboardButton
					message, keyboards = parseServiceCommand(txt)

					if keyboards != nil {
						options["reply_markup"] = bot.InlineKeyboardMarkup{
							InlineKeyboard: keyboards,
						}
					}
				} else {
					message = messageNoControllableServices
				}
			case strings.HasPrefix(txt, commandStatus):
				message = getStatus()
			case strings.HasPrefix(txt, commandHelp):
				message = getHelp()
				options["reply_markup"] = bot.InlineKeyboardMarkup{ // inline keyboard for link to github page
					InlineKeyboard: [][]bot.InlineKeyboardButton{
						bot.NewInlineKeyboardButtonsWithURL(map[string]string{
							"GitHub": githubPageUrl,
						}),
					},
				}
			// fallback
			default:
				if len(txt) > 0 {
					message = fmt.Sprintf("*%s*: %s", txt, messageUnknownCommand)
				} else {
					message = messageUnknownCommand
				}
			}
		}

		// send message
		if sent := b.SendMessage(update.Message.Chat.ID, message, options); sent.Ok {
			result = true
		} else {
			log.Printf("*** Failed to send message: %s", *sent.Description)
		}
	} else {
		log.Printf("*** Session does not exist for id: %s", userId)
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
	if strings.HasPrefix(txt, commandCancel) { // cancel command
		message = ""
	} else if strings.HasPrefix(txt, commandServiceStart) || strings.HasPrefix(txt, commandServiceStop) { // service
		message, _ = parseServiceCommand(txt)
	} else {
		log.Printf("*** Unprocessable callback query: %s", txt)

		return result // == false
	}

	// answer callback query
	options := map[string]interface{}{}
	if len(message) > 0 {
		options["text"] = message
	}
	if apiResult := b.AnswerCallbackQuery(query.ID, options); apiResult.Ok {
		// edit message and remove inline keyboards
		options := map[string]interface{}{
			"chat_id":    query.Message.Chat.ID,
			"message_id": query.Message.MessageID,
		}

		if len(message) <= 0 {
			message = messageCanceled
		}
		if apiResult := b.EditMessageText(message, options); apiResult.Ok {
			result = true
		} else {
			log.Printf("*** Failed to edit message text: %s", *apiResult.Description)
		}
	} else {
		log.Printf("*** Failed to answer callback query: %+v", query)
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
		log.Printf("Launching bot: @%s (%s)", *me.Result.Username, me.Result.FirstName)

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
					log.Printf("*** Error while receiving update (%s)", err)
				}
			})
		} else {
			panic("Failed to delete webhook")
		}
	} else {
		panic("Failed to get info of the bot")
	}
}
