package storage

import "time"

type Credential struct {
	ID             int64
	Connector      string
	KeyName        string
	EncryptedValue []byte
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Job struct {
	ID                   int64
	Name                 string
	CronExpr             string
	Timezone             string
	Task                 string
	Connectors           []string
	Permissions          map[string]string
	CostLimitUSD         float64
	OnFailure            string
	Enabled              bool
	TimeoutSeconds       int
	AllowOverlap         bool
	RunOnStartupIfMissed bool
	LastRunAt            *time.Time
	NextRunAt            *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type JobRun struct {
	ID            int64
	JobID         *int64
	JobName       string
	Status        string
	DurationMS    int64
	CostUSD       float64
	Error         string
	Result        string
	TriggerSource string
	StartedAt     time.Time
	FinishedAt    *time.Time
}

type AuditEntry struct {
	ID            int64
	Timestamp     time.Time
	TriggerSource string
	TriggerRef    string
	Connector     string
	Operation     string
	Resource      string
	Query         string
	Result        string
	DurationMS    int64
	Error         string
	Context       map[string]any
}

type LLMUsage struct {
	ID           int64
	Timestamp    time.Time
	RequestID    string
	JobRunID     *int64
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	DurationMS   int64
	Error        string
}

type MemoryEntry struct {
	ID        int64
	SessionID string
	Key       string
	Value     string
	Metadata  map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}
