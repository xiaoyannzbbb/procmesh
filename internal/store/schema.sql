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
    _stub INTEGER
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
    _stub INTEGER
);

CREATE TABLE IF NOT EXISTS audit_events (
    _stub INTEGER
);
