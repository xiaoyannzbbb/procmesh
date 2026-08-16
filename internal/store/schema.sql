CREATE TABLE IF NOT EXISTS local_meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS process_specs (
    process_id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    latest_revision INTEGER NOT NULL,
    spec_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS process_instances (
    instance_id TEXT PRIMARY KEY,
    process_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    pid INTEGER NOT NULL,
    shim_pid INTEGER NOT NULL,
    desired TEXT NOT NULL,
    observed TEXT NOT NULL,
    health TEXT NOT NULL,
    started_at TEXT,
    exit_at TEXT,
    exit_code INTEGER,
    restart_count INTEGER NOT NULL,
    active_revision INTEGER NOT NULL,
    boot_id TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_revisions (
    process_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    operator TEXT NOT NULL,
    ts TEXT NOT NULL,
    diff TEXT NOT NULL,
    comment TEXT NOT NULL,
    spec_json TEXT NOT NULL,
    PRIMARY KEY (process_id, revision)
);

CREATE TABLE IF NOT EXISTS operation_journal (
    operation_id TEXT PRIMARY KEY,
    operator TEXT NOT NULL,
    source_agent TEXT NOT NULL,
    target TEXT NOT NULL,
    type TEXT NOT NULL,
    request_payload BLOB,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    status TEXT NOT NULL,
    result BLOB,
    error TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    audit_id TEXT PRIMARY KEY,
    timestamp TEXT NOT NULL,
    user_id TEXT NOT NULL,
    username TEXT NOT NULL,
    source_ip TEXT NOT NULL,
    source_agent TEXT NOT NULL,
    target_agent TEXT NOT NULL,
    resource TEXT NOT NULL,
    action TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    result TEXT NOT NULL,
    metadata BLOB
);

CREATE TABLE IF NOT EXISTS batches (
    batch_id TEXT PRIMARY KEY,
    operator TEXT NOT NULL,
    source_agent TEXT NOT NULL,
    type TEXT NOT NULL,
    selector_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL,
    summary_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS batch_targets (
    batch_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    process_id TEXT NOT NULL,
    process_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    expected_revision INTEGER NOT NULL DEFAULT 0,
    payload_json TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    PRIMARY KEY (batch_id, operation_id)
);

CREATE INDEX IF NOT EXISTS batch_targets_batch ON batch_targets(batch_id);
CREATE INDEX IF NOT EXISTS batch_targets_incomplete ON batch_targets(status);
