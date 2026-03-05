package main

import (
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
)

func promptSelect(title string, options []menuOption, defaultIndex int) (int, bool) {
	if len(options) == 0 {
		return 0, false
	}
	if !isInteractiveTerminal() {
		return 0, false
	}
	if defaultIndex < 0 || defaultIndex >= len(options) {
		defaultIndex = 0
	}

	items := make([]string, 0, len(options))
	for _, option := range options {
		if option.Description != "" {
			items = append(items, fmt.Sprintf("%s — %s", option.Label, option.Description))
		} else {
			items = append(items, option.Label)
		}
	}

	prompt := promptui.Select{
		Label:     title,
		Items:     items,
		CursorPos: defaultIndex,
		Size:      minInt(12, len(items)),
	}
	index, _, err := prompt.Run()
	if err != nil {
		return 0, false
	}
	return index, true
}

func isInteractiveTerminal() bool {
	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (stdinInfo.Mode()&os.ModeCharDevice) != 0 && (stdoutInfo.Mode()&os.ModeCharDevice) != 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
