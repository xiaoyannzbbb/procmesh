# Process Config Schema-Driven Dual Editor Design

Date: 2026-08-17
Status: Approved
Scope: Process configuration editor in the Web UI

## 1. Goal

Replace the source-only process configuration editor with a schema-driven field form while retaining a full YAML editing mode. Both modes must cover every editable field in `procmesh.v1.ProcessSpec`, preserve protobuf field and numeric semantics, and submit through the existing revision-based `UpdateConfig` flow.

The editor remains a wide right-side drawer. The existing read-only configuration view, revision history, diff, rollback, conflict handling, and permission checks remain in place.

## 2. Design Choice

Use a typed UI schema tailored to `ProcessSpec`, backed by pure conversion and validation utilities.

This is preferred over descriptor-generated forms because protobuf descriptors cannot express domain labels, units, conditional health-check fields, collection editors, or backend validation rules. It is preferred over a fully hand-written page because a central schema provides a reviewable field inventory and prevents display, form, and validation behavior from drifting apart.

## 3. Architecture

### 3.1 Modules

- `processConfigSchema.ts`: section and field metadata, option sets, unit labels, visibility rules, and the complete editable-field inventory.
- `processConfigForm.ts`: form-state types, `ProcessSpec` conversion, CLI-compatible YAML conversion, normalization, and validation.
- `ProcessConfigForm.vue`: section layout and specialized collection editors for arguments, environment variables, and dependencies.
- `ProcessConfigPanel.vue`: server state, drawer lifecycle, mode synchronization, saving, conflicts, and unsaved-change protection.

The schema describes scalar fields. Nested repeatable structures use focused Vue renderers because their add/remove/reorder behavior cannot be represented safely by a generic scalar control alone.

### 3.2 Canonical Draft

The drawer owns one canonical `ProcessSpec` draft plus the active mode:

1. Opening the drawer clones the last accepted server spec.
2. Form controls edit a form representation derived from the canonical draft.
3. Entering YAML mode converts the canonical draft with `toJson(ProcessSpecSchema, draft, { useProtoFieldName: true })`, then serializes that value as YAML.
4. Leaving YAML mode parses the YAML document, converts it with `fromJson(ProcessSpecSchema, value)`, and validates the result.
5. A failed parse or validation blocks the mode switch and retains the current content.
6. Saving first synchronizes the active mode, then restores server-owned `processId` and `latestRevision` from the loaded spec before calling `UpdateConfig`.

This avoids independent drafts and silent last-writer-wins behavior between modes.

## 4. Drawer Experience

The drawer header contains a two-option segmented control: Field form and YAML. The form is the default mode.

The form uses progressive disclosure with the following sections:

1. Identity: name, owner agent, group, plus read-only process ID and revision.
2. Execution: command, arguments, working directory, run-as user.
3. Runtime: instances, autostart, startup priority, stop/kill signals, stop timeout.
4. Restart policy: mode, retry limits/window, and backoff settings.
5. Health check: check type and conditionally visible type-specific fields, followed by timing, thresholds, and restart behavior.
6. Logs and resources: rotation, compression, CPU, memory, and open-file limits.
7. Environment: add/remove key-value rows with duplicate-key validation.
8. Dependencies: add/remove process-condition rows with duplicate-process validation.

Labels remain visible. Numeric values show explicit API units such as milliseconds, seconds, and bytes; values are not silently rescaled. Boolean values use switches or checkboxes. Known option sets use selects. Row editors use icon buttons with accessible labels for removal.

The drawer footer remains sticky and contains comment, cancel, and the single primary save action. Closing a dirty editor retains the current confirmation behavior.

## 5. Complete Field Coverage

| Message | Field | Form behavior |
| --- | --- | --- |
| `ProcessSpec` | `process_id` | Read-only |
| `ProcessSpec` | `name` | Required text |
| `ProcessSpec` | `owner_agent_id` | Text |
| `ProcessSpec` | `group` | Text |
| `ProcessSpec` | `command` | Required text |
| `ProcessSpec` | `args` | Ordered string rows |
| `ProcessSpec` | `working_directory` | Path text |
| `ProcessSpec` | `run_as_user` | Text |
| `ProcessSpec` | `environment` | Key-value rows |
| `ProcessSpec` | `instances` | Integer, minimum 1 |
| `ProcessSpec` | `autostart` | Boolean |
| `ProcessSpec` | `stop_signal` | Text |
| `ProcessSpec` | `kill_signal` | Text |
| `ProcessSpec` | `stop_timeout_ms` | Non-negative integer, ms |
| `ProcessSpec` | `startup_priority` | Integer |
| `ProcessSpec` | `restart` | Restart policy group |
| `ProcessSpec` | `health` | Health-check group |
| `ProcessSpec` | `log` | Log policy group |
| `ProcessSpec` | `resources` | Resource limit group |
| `ProcessSpec` | `dependencies` | Dependency rows |
| `ProcessSpec` | `latest_revision` | Read-only |
| `RestartPolicy` | `mode` | `never`, `always`, `on-failure` |
| `RestartPolicy` | `max_retries` | Non-negative integer |
| `RestartPolicy` | `retry_window_ms` | Non-negative integer, ms |
| `RestartPolicy` | `backoff` | Backoff group |
| `Backoff` | `initial_ms` | Non-negative integer, ms |
| `Backoff` | `max_ms` | Non-negative integer, ms |
| `Backoff` | `multiplier` | Non-negative decimal; zero or at least 1 |
| `HealthCheck` | `type` | `alive`, `http`, `tcp`, `exec` |
| `HealthCheck` | `url` | Required for HTTP |
| `HealthCheck` | `method` | HTTP method |
| `HealthCheck` | `address` | Required for TCP |
| `HealthCheck` | `command` | Required for exec |
| `HealthCheck` | `expected_status` | HTTP status integer |
| `HealthCheck` | `args` | Ordered exec argument rows |
| `HealthCheck` | `initial_delay_ms` | Non-negative integer, ms |
| `HealthCheck` | `interval_ms` | Non-negative integer, ms |
| `HealthCheck` | `timeout_ms` | Non-negative integer, ms |
| `HealthCheck` | `failure_threshold` | Non-negative integer |
| `HealthCheck` | `success_threshold` | Non-negative integer |
| `HealthCheck` | `restart_on_failure` | Boolean |
| `HealthCheck` | `restart_cooldown_ms` | Non-negative integer, ms |
| `LogPolicy` | `max_size` | Non-negative integer, bytes |
| `LogPolicy` | `max_files` | Non-negative integer |
| `LogPolicy` | `max_age_seconds` | Non-negative integer, seconds |
| `LogPolicy` | `compress` | Boolean |
| `ResourceLimit` | `cpu_quota_millis` | Non-negative integer |
| `ResourceLimit` | `memory_bytes` | Non-negative integer, bytes |
| `ResourceLimit` | `open_files` | Non-negative integer |
| `Dependency` | `process_name` | Required text, unique per dependency list |
| `Dependency` | `condition` | `STARTED` or `HEALTHY` |

Fields hidden by a health-check type remain in the draft and YAML representation. Changing the type does not erase them, so switching modes cannot lose values.

## 6. Validation and Errors

Validation mirrors known backend rules and adds only structural constraints required by the form:

- Name matches `^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`.
- Group is empty or, after trimming, matches `^[A-Za-z0-9._-]{1,64}$`.
- Command is required and instances is at least 1.
- A positive retry count requires a positive retry window.
- A nonzero backoff multiplier must be at least 1.
- HTTP checks require an `http` or `https` URL; TCP checks require an address; exec checks require a command.
- Environment keys and dependency process names cannot be duplicated.
- Numeric protobuf fields must be finite integers in their declared `int32` or safe `int64` range. The UI does not accept scientific notation for integer fields.

Validation runs on blur for individual fields and on mode switch/save for the whole form. Inline errors are associated with their controls. A failed full validation shows a focusable summary linked to invalid fields and focuses the first invalid item. YAML parse or protobuf conversion errors remain directly below the YAML editor.

Server errors and revision conflicts use the existing banners. A conflict does not replace either draft or expected revision until the user explicitly reloads.

## 7. Compatibility and Defaults

The editor does not invent or apply backend defaults. Zero and empty values received from the API remain visible and round-trip unchanged. Help text may describe effective defaults, but saving an untouched spec must produce an equivalent protobuf message.

The YAML mode uses protobuf field names (`snake_case`) so its output matches CLI process files. Buf's protobuf JSON conversion remains the typed intermediate contract, including string encoding for `int64` values, and `fromJson` continues to reject unknown fields. YAML comments and formatting are accepted, while semantic dirty checks compare canonical protobuf values.

## 8. Testing

Pure utility tests will:

- Construct a fully populated `ProcessSpec` containing every field and verify spec-to-form-to-spec equality.
- Verify CLI-compatible YAML round trips, including `int64`, maps, repeated fields, nested messages, comments, and unknown-field rejection.
- Exercise every validation rule and health-type visibility rule.
- Assert that the schema inventory covers all editable `ProcessSpec` paths.

Component tests will verify:

- The form is the default mode and renders all sections.
- Collection rows add and remove correctly.
- Valid edits survive both mode switches.
- Invalid form or YAML content blocks switching and saving with accessible errors.
- Read-only fields cannot be edited and are restored before submission.
- Save, conflict, refetch protection, unsaved-close confirmation, and revision behavior continue to work.

Finally, run the focused Vitest suite, the full Web test suite, i18n validation, and the production Web build. Browser verification covers the actual process configuration route at desktop and narrow viewport widths, including drawer scrolling, sticky actions, keyboard focus, and both themes if supported by the running app.

## 9. Non-Goals

- Changing protobuf messages or backend validation/default semantics.
- Adding automatic unit conversion or human-readable storage size parsing.
- Adding an external schema or code editor package.
- Changing revision history, diff, rollback, or the read-only page layout beyond adjustments needed to host the new editor.
