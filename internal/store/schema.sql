CREATE TABLE IF NOT EXISTS local_meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

-- Stub tables: later tasks replace these with full columns.
CREATE TABLE IF NOT EXISTS process_specs (
    _stub INTEGER
);

CREATE TABLE IF NOT EXISTS process_instances (
    _stub INTEGER
);

CREATE TABLE IF NOT EXISTS config_revisions (
    _stub INTEGER
);

CREATE TABLE IF NOT EXISTS operation_journal (
    _stub INTEGER
);

CREATE TABLE IF NOT EXISTS audit_events (
    _stub INTEGER
);
