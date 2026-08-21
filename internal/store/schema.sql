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
    boot_id TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT ''
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

CREATE TABLE IF NOT EXISTS metric_samples (
    series TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    layer TEXT NOT NULL,
    ts_unix INTEGER NOT NULL,
    value REAL NOT NULL,
    PRIMARY KEY (series, subject_id, layer, ts_unix)
);
CREATE INDEX IF NOT EXISTS metric_samples_query
    ON metric_samples(subject_id, layer, series, ts_unix);

CREATE TABLE IF NOT EXISTS alerts (
    alert_id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    severity TEXT NOT NULL,
    node_id TEXT NOT NULL,
    process_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    state TEXT NOT NULL,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    notified_at TEXT,
    resolved_at TEXT,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS alerts_last_at ON alerts(last_at DESC);
CREATE INDEX IF NOT EXISTS alerts_state ON alerts(state);

CREATE TABLE IF NOT EXISTS backup_index (
    snapshot_id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    process_ids_json TEXT NOT NULL,
    revision_range_json TEXT NOT NULL,
    sha256 TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0,
	sink TEXT NOT NULL,
	destination_profile TEXT NOT NULL DEFAULT '',
	location TEXT NOT NULL,
    source_node_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    policy_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS backup_index_created ON backup_index(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS backup_index_task ON backup_index(run_id, task_id) WHERE run_id != '' AND task_id != '';
