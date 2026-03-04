package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/agent"
	"github.com/MRL-00/viaduct-ai/internal/config"
	"github.com/MRL-00/viaduct-ai/internal/connector"
	"github.com/MRL-00/viaduct-ai/internal/storage"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	logger               *slog.Logger
	cfg                  config.Config
	store                *storage.Store
	agent                *agent.Agent
	registry             *connector.Registry
	connectorPermissions map[string]string

	cron     *cron.Cron
	entryIDs map[string]cron.EntryID
	running  map[string]bool
	jobs     map[string]storage.Job
	mu       sync.Mutex
}

func New(logger *slog.Logger, cfg config.Config, store *storage.Store, agent *agent.Agent, registry *connector.Registry) (*Scheduler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	location, err := time.LoadLocation(cfg.Scheduler.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load scheduler timezone: %w", err)
	}

	connectorPermissions := map[string]string{}
	for name, connectorCfg := range cfg.Connectors {
		level := "read"
		if len(connectorCfg.Permissions) > 0 {
			level = connectorCfg.Permissions[0]
		}
		connectorPermissions[name] = level
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	engine := cron.New(
		cron.WithLocation(location),
		cron.WithParser(parser),
	)

	return &Scheduler{
		logger:               logger,
		cfg:                  cfg,
		store:                store,
		agent:                agent,
		registry:             registry,
		connectorPermissions: connectorPermissions,
		cron:                 engine,
		entryIDs:             make(map[string]cron.EntryID),
		running:              make(map[string]bool),
		jobs:                 make(map[string]storage.Job),
	}, nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.loadJobs(ctx); err != nil {
		return err
	}
	s.cron.Start()
	s.logger.Info("scheduler started", "jobs", len(s.jobs))
	return nil
}

func (s *Scheduler) Stop(ctx context.Context) {
	shutdownCtx := s.cron.Stop()
	select {
	case <-ctx.Done():
	case <-shutdownCtx.Done():
	}
}

func (s *Scheduler) loadJobs(ctx context.Context) error {
	for _, cfgJob := range s.cfg.Jobs {
		enabled := true
		if cfgJob.Enabled != nil {
			enabled = *cfgJob.Enabled
		}
		timeoutSeconds := 300
		if cfgJob.Timeout != "" {
			timeout, err := time.ParseDuration(cfgJob.Timeout)
			if err != nil {
				return fmt.Errorf("parse timeout for job %s: %w", cfgJob.Name, err)
			}
			timeoutSeconds = int(timeout.Seconds())
		}
		job := storage.Job{
			Name:                 cfgJob.Name,
			CronExpr:             cfgJob.Cron,
			Timezone:             firstNonEmpty(cfgJob.Timezone, s.cfg.Scheduler.Timezone),
			Task:                 cfgJob.Task,
			Connectors:           cfgJob.Connectors,
			Permissions:          cfgJob.Permissions,
			CostLimitUSD:         cfgJob.CostLimitUSD,
			OnFailure:            cfgJob.OnFailure,
			Enabled:              enabled,
			TimeoutSeconds:       timeoutSeconds,
			AllowOverlap:         cfgJob.AllowOverlap,
			RunOnStartupIfMissed: cfgJob.RunOnStartupIfMissed,
		}
		if err := s.store.Jobs.Upsert(ctx, job); err != nil {
			return err
		}
	}

	jobs, err := s.store.Jobs.List(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range jobs {
		s.jobs[job.Name] = job
		if !job.Enabled {
			continue
		}
		entryID, err := s.cron.AddFunc(job.CronExpr, func() {
			s.execute(context.Background(), job, "cron", job.Name)
		})
		if err != nil {
			return fmt.Errorf("schedule job %s: %w", job.Name, err)
		}
		s.entryIDs[job.Name] = entryID
		next := s.cron.Entry(entryID).Next
		if err := s.store.Jobs.UpdateRunTimes(ctx, job.Name, job.LastRunAt, &next); err != nil {
			s.logger.Warn("failed to update next run time", "job", job.Name, "error", err)
		}
	}
	return nil
}

func (s *Scheduler) ListJobs() []storage.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]storage.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

func (s *Scheduler) RunNow(ctx context.Context, name string) error {
	job, err := s.store.Jobs.GetByName(ctx, name)
	if err != nil {
		return err
	}
	go s.execute(ctx, job, "manual", name)
	return nil
}

func (s *Scheduler) History(ctx context.Context, name string, limit int) ([]storage.JobRun, error) {
	return s.store.JobRuns.ListByJobName(ctx, name, limit)
}

func (s *Scheduler) SetEnabled(ctx context.Context, name string, enabled bool) error {
	job, err := s.store.Jobs.GetByName(ctx, name)
	if err != nil {
		return err
	}
	job.Enabled = enabled
	if err := s.store.Jobs.Upsert(ctx, job); err != nil {
		return err
	}
	if enabled {
		if _, ok := s.entryIDs[name]; ok {
			return nil
		}
		entryID, err := s.cron.AddFunc(job.CronExpr, func() {
			s.execute(context.Background(), job, "cron", job.Name)
		})
		if err != nil {
			return err
		}
		s.entryIDs[name] = entryID
	} else {
		if entryID, ok := s.entryIDs[name]; ok {
			s.cron.Remove(entryID)
			delete(s.entryIDs, name)
		}
	}
	return nil
}

func (s *Scheduler) execute(ctx context.Context, job storage.Job, triggerSource, triggerRef string) {
	if !job.AllowOverlap {
		s.mu.Lock()
		if s.running[job.Name] {
			s.mu.Unlock()
			s.recordSkipped(context.Background(), job, triggerSource, "previous run still in progress")
			return
		}
		s.running[job.Name] = true
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.running, job.Name)
			s.mu.Unlock()
		}()
	}

	startedAt := time.Now().UTC()
	runID, err := s.store.JobRuns.Create(context.Background(), storage.JobRun{
		JobID:         &job.ID,
		JobName:       job.Name,
		Status:        "running",
		TriggerSource: triggerSource,
		StartedAt:     startedAt,
	})
	if err != nil {
		s.logger.Error("failed to create job run", "job", job.Name, "error", err)
		return
	}

	if err := s.preFlightChecks(ctx, job); err != nil {
		_ = s.store.JobRuns.Complete(context.Background(), runID, "skipped", "", time.Since(startedAt), 0, err)
		s.recordAudit(job, triggerSource, triggerRef, "error", err, time.Since(startedAt), map[string]any{"phase": "preflight"})
		return
	}

	timeout := 5 * time.Minute
	if job.TimeoutSeconds > 0 {
		timeout = time.Duration(job.TimeoutSeconds) * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, execErr := s.agent.Execute(execCtx, agent.TaskRequest{
		Goal:          job.Task,
		TaskType:      "analysis",
		TriggerSource: triggerSource,
		TriggerRef:    triggerRef,
		Context: map[string]any{
			"job_name": job.Name,
		},
	})

	status := "success"
	resultText := result.Response
	if execErr != nil {
		status = "failed"
	}
	if execCtx.Err() == context.DeadlineExceeded {
		status = "timed_out"
		execErr = execCtx.Err()
	}

	duration := time.Since(startedAt)
	if err := s.store.JobRuns.Complete(context.Background(), runID, status, resultText,
		duration, result.TotalCostUSD, execErr); err != nil {
		s.logger.Error("failed to complete job run", "job", job.Name, "run_id", runID, "error", err)
	}

	now := time.Now().UTC()
	entryID, hasEntry := s.entryIDs[job.Name]
	var next *time.Time
	if hasEntry {
		n := s.cron.Entry(entryID).Next
		next = &n
	}
	if err := s.store.Jobs.UpdateRunTimes(context.Background(), job.Name, &now, next); err != nil {
		s.logger.Warn("failed to update run times", "job", job.Name, "error", err)
	}

	if execErr != nil {
		s.recordAudit(job, triggerSource, triggerRef, "error", execErr, duration, map[string]any{"status": status})
	} else {
		s.recordAudit(job, triggerSource, triggerRef, "success", nil, duration, map[string]any{
			"status": status, "cost_usd": result.TotalCostUSD,
		})
	}
}

func (s *Scheduler) preFlightChecks(ctx context.Context, job storage.Job) error {
	for _, connectorName := range job.Connectors {
		c, ok := s.registry.Get(connectorName)
		if !ok {
			return fmt.Errorf("connector %s not registered", connectorName)
		}
		if err := c.HealthCheck(ctx); err != nil {
			return fmt.Errorf("connector %s unhealthy: %w", connectorName, err)
		}
		requested := job.Permissions[connectorName]
		if requested == "" {
			requested = "read"
		}
		configured := s.connectorPermissions[connectorName]
		if configured == "" {
			configured = "read"
		}
		if permissionRank(requested) > permissionRank(configured) {
			return fmt.Errorf("connector %s job permission %s exceeds configured permission %s", connectorName, requested, configured)
		}
	}
	return nil
}

func (s *Scheduler) recordSkipped(ctx context.Context, job storage.Job, triggerSource, reason string) {
	start := time.Now().UTC()
	runID, err := s.store.JobRuns.Create(ctx, storage.JobRun{
		JobID:         &job.ID,
		JobName:       job.Name,
		Status:        "running",
		TriggerSource: triggerSource,
		StartedAt:     start,
	})
	if err != nil {
		return
	}
	runErr := fmt.Errorf("%s", reason)
	_ = s.store.JobRuns.Complete(ctx, runID, "skipped", "", time.Since(start), 0, runErr)
	s.recordAudit(job, triggerSource, job.Name, "error", runErr, time.Since(start), map[string]any{"status": "skipped"})
}

func (s *Scheduler) recordAudit(job storage.Job, triggerSource, triggerRef, result string, err error, duration time.Duration, fields map[string]any) {
	if s.store == nil || s.store.Audit == nil {
		return
	}
	entry := storage.AuditEntry{
		Timestamp:     time.Now().UTC(),
		TriggerSource: triggerSource,
		TriggerRef:    triggerRef,
		Connector:     "scheduler",
		Operation:     "job_execute",
		Resource:      job.Name,
		Result:        result,
		DurationMS:    duration.Milliseconds(),
		Context:       fields,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	_, _ = s.store.Audit.Insert(context.Background(), entry)
}

func permissionRank(level string) int {
	switch level {
	case "admin":
		return 3
	case "write":
		return 2
	default:
		return 1
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
