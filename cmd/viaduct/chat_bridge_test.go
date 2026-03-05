package main

import "testing"

func TestNormalizeInboundGoal(t *testing.T) {
	tests := []struct {
		name          string
		connectorName string
		content       string
		want          string
	}{
		{
			name:          "slack mention is stripped",
			connectorName: "slack",
			content:       "<@U12345> Summarize Azure alerts",
			want:          "Summarize Azure alerts",
		},
		{
			name:          "slack multiple mentions and whitespace",
			connectorName: "slack",
			content:       "  <@U12345>   <@W8888>   run   the   briefing  ",
			want:          "run the briefing",
		},
		{
			name:          "non slack message unchanged except whitespace normalization",
			connectorName: "microsoft365",
			content:       "   please    investigate   incident   ",
			want:          "please investigate incident",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeInboundGoal(tt.connectorName, tt.content)
			if got != tt.want {
				t.Fatalf("normalizeInboundGoal() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		value string
		max   int
		want  string
	}{
		{name: "short string unchanged", value: "hello", max: 10, want: "hello"},
		{name: "truncates and appends ellipsis", value: "abcdefghij", max: 7, want: "abcd..."},
		{name: "handles small max", value: "abcdef", max: 3, want: "abc"},
		{name: "nonpositive max", value: "abcdef", max: 0, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.value, tt.max)
			if got != tt.want {
				t.Fatalf("truncateRunes() = %q, want %q", got, tt.want)
			}
		})
	}
}
