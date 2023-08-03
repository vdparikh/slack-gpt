package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/vdparikh/slack-gpt/pkg/gpt"
	"gopkg.in/yaml.v2"
)

type Config struct {
	BlockedKeywords []string `yaml:"blocked_keywords"`
	RegexPatterns   []string `yaml:"regex_patterns"`
}

var (
	gptApiKey string
	client    *socketmode.Client
	gptClient *gpt.GPT
)

var (
	blockedKeywords []string
	compiledRegexes []*regexp.Regexp
)

func init() {

	gptApiKey = os.Getenv("GPT_API_KEY")
	if gptApiKey == "" {
		fmt.Fprintf(os.Stderr, "GPT_API_KEY must be set.\n")
		os.Exit(1)
	}

	appToken := os.Getenv("SLACK_APP_TOKEN")
	if appToken == "" {
		fmt.Fprintf(os.Stderr, "SLACK_APP_TOKEN must be set.\n")
		os.Exit(1)
	}

	if !strings.HasPrefix(appToken, "xapp-") {
		fmt.Fprintf(os.Stderr, "SLACK_APP_TOKEN must have the prefix \"xapp-\".")
	}

	botToken := os.Getenv("SLACK_BOT_TOKEN")
	if botToken == "" {
		fmt.Fprintf(os.Stderr, "SLACK_BOT_TOKEN must be set.\n")
		os.Exit(1)
	}

	if !strings.HasPrefix(botToken, "xoxb-") {
		fmt.Fprintf(os.Stderr, "SLACK_BOT_TOKEN must have the prefix \"xoxb-\".")
	}

	api := slack.New(
		botToken,
		slack.OptionDebug(false),
		// slack.OptionLog(log.New(os.Stdout, "api: ", log.Lshortfile|log.LstdFlags)),
		slack.OptionAppLevelToken(appToken),
	)

	client = socketmode.New(
		api,
		socketmode.OptionDebug(false),
		// socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
	)
}

func loadConfig(file string) (*Config, error) {
	bytes, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(bytes, &config)
	return &config, err
}

func blockMessage(text string) bool {
	lowercaseText := strings.ToLower(text)

	for _, keyword := range blockedKeywords {

		if strings.Contains(lowercaseText, keyword) {
			return true
		}

	}

	for _, regex := range compiledRegexes {
		if regex.MatchString(lowercaseText) {
			return true
		}
	}

	return false
}

func setupConfigWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()
	err = watcher.Add("config.yaml")
	if err != nil {
		log.Error().Msg(err.Error())
	}

	go func(watcher *fsnotify.Watcher) {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Print("event:", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Print("modified file:", event.Name)
					config, err := loadConfig("config.yaml")
					if err != nil {
						log.Print("Failed to load config:", err)
					} else {
						blockedKeywords = config.BlockedKeywords
						for _, pattern := range config.RegexPatterns {
							compiledRegex, err := regexp.Compile(pattern)
							if err != nil {
								panic("Failed to compile regex pattern: " + err.Error())
							}
							compiledRegexes = append(compiledRegexes, compiledRegex)
						}
						log.Print("Config reloaded")

					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Print("error:", err)
			}
		}

	}(watcher)

	config, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatal().Msg(err.Error())
	}

	blockedKeywords = config.BlockedKeywords
	for _, pattern := range config.RegexPatterns {
		compiledRegex, err := regexp.Compile(pattern)
		if err != nil {
			panic("Failed to compile regex pattern: " + err.Error())
		}
		compiledRegexes = append(compiledRegexes, compiledRegex)
	}

	return nil
}

func handleEvent(evt socketmode.Event) error {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		fmt.Println("Connecting to Slack with Socket Mode...")
	case socketmode.EventTypeConnectionError:
		fmt.Println("Connection failed. Retrying later...")
	case socketmode.EventTypeConnected:
		fmt.Println("Connected to Slack with Socket Mode.")
	case socketmode.EventTypeEventsAPI:
		if err := handleEventsAPI(evt); err != nil {
			return fmt.Errorf("error handling Events API: %w", err)
		}
	case socketmode.EventTypeInteractive:
		if err := handleInteractive(evt); err != nil {
			return fmt.Errorf("error handling interactive event: %w", err)
		}
	case socketmode.EventTypeSlashCommand:
		if err := handleSlashCommand(evt); err != nil {
			return fmt.Errorf("error handling slash command: %w", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unexpected event type received: %s\n", evt.Type)
	}

	return nil
}

func handleEventsAPI(evt socketmode.Event) error {
	eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return fmt.Errorf("ignored event: %+v", evt)
	}

	client.Ack(*evt.Request)

	switch eventsAPIEvent.Type {
	case slackevents.CallbackEvent:
		return handleCallbackEvent(eventsAPIEvent.InnerEvent)
	default:
		client.Debugf("unsupported Events API event received")
	}

	return nil
}

func handleCallbackEvent(innerEvent slackevents.EventsAPIInnerEvent) error {
	switch ev := innerEvent.Data.(type) {
	case *slackevents.AppHomeOpenedEvent:
		return handleAppHomeOpenedEvent(ev)
	case *slackevents.AppMentionEvent:
		return handleAppMentionEvent(ev)
	case *slackevents.MessageEvent:
		return handleMessageEvent(ev)
	case *slackevents.MemberJoinedChannelEvent:
		return handleMemberJoinedChannelEvent(ev)
	default:
		return fmt.Errorf("unsupported callback event received")
	}
}

func handleAppHomeOpenedEvent(ev *slackevents.AppHomeOpenedEvent) error {
	fmt.Println("handleAppHomeOpenedEvent")
	channelId := ev.Channel
	message := "Hello, welcome to the Slack GPT! Avoid sending any company proprietary into prompts. Please note that this is a work in progress and may not always work as expected."

	_, _, err := client.PostMessage(channelId, slack.MsgOptionText(message, false))
	if err != nil {
		return fmt.Errorf("failed posting message to channel %s: %v", channelId, err)
	}

	return nil
}
func processMessage(channel string, text string, userId string, timestamp string, threadTimestamp string, isPartOfThread bool) error {

	start := time.Now()
	userInput := text
	block := blockMessage(userInput)

	defer func() {
		duration := time.Since(start).String()
		log.Debug().Str("user", userId).Str("timestamp", timestamp).Str("block", strconv.FormatBool(block)).Str("duration", duration).Str("input", userInput).Msg("request")
	}()

	log.Debug().Str("user", userId).Str("timestamp", timestamp).Str("block", strconv.FormatBool(block)).Str("input", userInput).Msg("request")

	if block {
		client.SendMessage(channel, slack.MsgOptionText("Your request was blocked because it contained a blocked keyword or sensitive data.", false), slack.MsgOptionTS(timestamp))
		return nil
	}

	_, tempMessageTimestamp, _, err := client.SendMessage(channel, slack.MsgOptionText("Your request is being processed...", false), slack.MsgOptionTS(timestamp))
	if err != nil {
		return fmt.Errorf("failed posting initial processing message: %v", err)
	}
	threadMessages := make([]string, 0)

	// If the reply is in thread then get the context
	if isPartOfThread {
		history, _, _, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
			ChannelID: channel,
			Timestamp: threadTimestamp,
		})

		if err != nil {
			log.Printf("Error getting conversation history: %v", err)
			client.PostEphemeral(channel, userId, slack.MsgOptionText(fmt.Sprintf("Error getting conversation history: %s", err.Error()), true), slack.MsgOptionAsUser(true))
		} else {
			for _, message := range history {
				if message.BotID == "U05L1SWMW4B" && message.Text != "Your request is being processed..." {
					threadMessages = append(threadMessages, message.Text)
				}
			}
		}
	}

	threadMessages = append(threadMessages, userInput)

	messageText, messageErr := gptClient.Invoke(threadMessages)

	if messageErr != nil {
		client.UpdateMessage(channel, tempMessageTimestamp, slack.MsgOptionText(fmt.Sprintf("ChatCompletion error: %s", messageErr.Error()), true), slack.MsgOptionAsUser(true))
		return fmt.Errorf("ChatCompletion error: %v", err)
	}

	gpt4Response := slack.NewSectionBlock(
		&slack.TextBlockObject{
			Type: slack.MarkdownType,
			Text: messageText,
		},
		nil, nil,
	)

	_, _, _, err = client.UpdateMessage(channel, tempMessageTimestamp, slack.MsgOptionBlocks(gpt4Response), slack.MsgOptionAsUser(true))
	if err != nil {
		return fmt.Errorf("error updating message: %v", err)
	}

	return nil
}

func handleAppMentionEvent(ev *slackevents.AppMentionEvent) error {
	isPartOfThread := ev.ThreadTimeStamp != "" && ev.ThreadTimeStamp != ev.TimeStamp
	return processMessage(ev.Channel, strings.TrimPrefix(ev.Text, "<@bot>"), ev.User, ev.TimeStamp, ev.ThreadTimeStamp, isPartOfThread)
}

func handleMessageEvent(ev *slackevents.MessageEvent) error {
	if ev.User != "U05L1SWMW4B" && ev.SubType != "message_changed" && ev.BotID == "" {
		isPartOfThread := ev.ThreadTimeStamp != "" && ev.ThreadTimeStamp != ev.TimeStamp
		return processMessage(ev.Channel, ev.Text, ev.User, ev.TimeStamp, ev.ThreadTimeStamp, isPartOfThread)
	}

	return nil
}

func handleMemberJoinedChannelEvent(ev *slackevents.MemberJoinedChannelEvent) error {
	fmt.Printf("user %q joined to channel %q", ev.User, ev.Channel)
	return nil
}

func handleInteractive(evt socketmode.Event) error {
	callback, ok := evt.Data.(slack.InteractionCallback)

	if !ok {
		return fmt.Errorf("ignored event: %+v", evt)
	}

	fmt.Printf("Interaction received: %+v\n", callback)

	var payload interface{}

	switch callback.Type {
	case slack.InteractionTypeBlockActions:
		client.Debugf("button clicked!")
	case slack.InteractionTypeShortcut:
	case slack.InteractionTypeViewSubmission:
	case slack.InteractionTypeDialogSubmission:
	default:

	}

	client.Ack(*evt.Request, payload)
	return nil
}

func handleSlashCommand(evt socketmode.Event) error {
	cmd, ok := evt.Data.(slack.SlashCommand)

	if !ok {
		return fmt.Errorf("ignored event: %+v", evt)
	}

	client.Debugf("Slash command received: %+v", cmd)

	payload := map[string]interface{}{
		"blocks": []slack.Block{
			slack.NewSectionBlock(
				&slack.TextBlockObject{
					Type: slack.MarkdownType,
					Text: "foo",
				},
				nil,
				slack.NewAccessory(
					slack.NewButtonBlockElement(
						"",
						"somevalue",
						&slack.TextBlockObject{
							Type: slack.PlainTextType,
							Text: "bar",
						},
					),
				),
			),
		},
	}

	client.Ack(*evt.Request, payload)

	return nil
}

func main() {

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	setupConfigWatcher()
	gptClient = gpt.Init(gptApiKey)

	go func() {
		for evt := range client.Events {
			err := handleEvent(evt)
			if err != nil {
				fmt.Printf("Error handling event: %v\n", err)
				continue
			}
		}
	}()

	client.Run()
}
