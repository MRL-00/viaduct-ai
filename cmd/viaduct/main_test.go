package main

import "testing"

func TestConnectorConfigReadySlack(t *testing.T) {
	if ok, _ := connectorConfigReady("slack", map[string]any{}); ok {
		t.Fatalf("expected slack connector to be not ready without bot_token")
	}
	if ok, _ := connectorConfigReady("slack", map[string]any{"bot_token": "xoxb-123"}); !ok {
		t.Fatalf("expected slack connector to be ready with bot_token")
	}
}
