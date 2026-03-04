package storage

import (
	"fmt"
	"time"
)

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", value, err)
	}
	return t, nil
}
