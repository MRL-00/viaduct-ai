CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  connector TEXT NOT NULL,
  key_name TEXT NOT NULL,
  encrypted_value BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(connector, key_name)
);

CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  cron_expr TEXT NOT NULL,
  timezone TEXT NOT NULL,
  task TEXT NOT NULL,
  connectors_json TEXT NOT NULL,
  permissions_json TEXT NOT NULL,
  cost_limit_usd REAL NOT NULL DEFAULT 0,
  on_failure TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  timeout_seconds INTEGER NOT NULL DEFAULT 300,
  allow_overlap INTEGER NOT NULL DEFAULT 0,
  run_on_startup_if_missed INTEGER NOT NULL DEFAULT 0,
  last_run_at TEXT,
  next_run_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS job_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id INTEGER,
  job_name TEXT NOT NULL,
  status TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  trigger_source TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  FOREIGN KEY(job_id) REFERENCES jobs(id)
);

CREATE INDEX IF NOT EXISTS idx_job_runs_job_name_started_at
  ON job_runs(job_name, started_at DESC);

CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp TEXT NOT NULL,
  trigger_source TEXT NOT NULL,
  trigger_ref TEXT NOT NULL DEFAULT '',
  connector TEXT NOT NULL,
  operation TEXT NOT NULL,
  resource TEXT NOT NULL DEFAULT '',
  query TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  context_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp
  ON audit_log(timestamp DESC);

CREATE TABLE IF NOT EXISTS llm_usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp TEXT NOT NULL,
  request_id TEXT NOT NULL,
  job_run_id INTEGER,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  cost_usd REAL NOT NULL,
  duration_ms INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_llm_usage_timestamp
  ON llm_usage(timestamp DESC);

CREATE TABLE IF NOT EXISTS memory (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(session_id, key)
);
