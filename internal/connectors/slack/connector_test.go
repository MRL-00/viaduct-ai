package slack

import (
	"testing"

	"github.com/slack-go/slack/slackevents"
)

func TestShouldHandleThreadReply(t *testing.T) {
	c := &Connector{
		botUserID:     "U_BOT",
		threadContext: map[string][]string{"123.456": {"123.456"}},
	}

	t.Run("accepts known thread user reply", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:            "U_USER",
			Text:            "follow up",
			ThreadTimeStamp: "123.456",
			TimeStamp:       "123.789",
			Channel:         "C123",
		}
		if !c.shouldHandleThreadReply(event) {
			t.Fatal("expected thread reply to be accepted")
		}
	})

	t.Run("rejects unknown thread", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:            "U_USER",
			Text:            "follow up",
			ThreadTimeStamp: "999.000",
		}
		if c.shouldHandleThreadReply(event) {
			t.Fatal("expected unknown thread to be ignored")
		}
	})

	t.Run("rejects bot self message", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:            "U_BOT",
			Text:            "reply",
			ThreadTimeStamp: "123.456",
		}
		if c.shouldHandleThreadReply(event) {
			t.Fatal("expected bot self message to be ignored")
		}
	})

	t.Run("rejects subtype message", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:            "U_USER",
			Text:            "edited",
			ThreadTimeStamp: "123.456",
			SubType:         "message_changed",
		}
		if c.shouldHandleThreadReply(event) {
			t.Fatal("expected subtype message to be ignored")
		}
	})

	t.Run("rejects direct message events", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:            "U_USER",
			Text:            "hello",
			ThreadTimeStamp: "123.456",
			ChannelType:     "im",
		}
		if c.shouldHandleThreadReply(event) {
			t.Fatal("expected direct message event to bypass thread reply handler")
		}
	})
}

func TestShouldHandleDirectMessage(t *testing.T) {
	c := &Connector{botUserID: "U_BOT"}

	t.Run("accepts user dm", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:        "U_USER",
			Text:        "hello",
			Channel:     "D123",
			ChannelType: "im",
			TimeStamp:   "123.456",
		}
		if !c.shouldHandleDirectMessage(event) {
			t.Fatal("expected direct message to be accepted")
		}
	})

	t.Run("rejects non-dm message", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:        "U_USER",
			Text:        "hello",
			ChannelType: "channel",
		}
		if c.shouldHandleDirectMessage(event) {
			t.Fatal("expected non-dm message to be ignored")
		}
	})

	t.Run("rejects bot self dm", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:        "U_BOT",
			Text:        "hello",
			ChannelType: "im",
		}
		if c.shouldHandleDirectMessage(event) {
			t.Fatal("expected bot dm to be ignored")
		}
	})

	t.Run("rejects subtype dm", func(t *testing.T) {
		event := &slackevents.MessageEvent{
			User:        "U_USER",
			Text:        "edited",
			ChannelType: "im",
			SubType:     "message_changed",
		}
		if c.shouldHandleDirectMessage(event) {
			t.Fatal("expected subtype dm to be ignored")
		}
	})
}
