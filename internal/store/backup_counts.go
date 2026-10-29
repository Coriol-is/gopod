package store

// rowCountSQL maps every ordinary table this binary creates (virtual tables and
// their shadow tables are excluded) to a constant row-count statement. Table
// names read from a database under inspection are never placed in SQL: only a
// name found in this map is counted, anything else is reported. The parity
// test TestRowCountSQLCoversEverySchemaTable fails when a table is added to the
// schema without being added here.
var rowCountSQL = map[string]string{
	"outbox_plain_fallbacks": "SELECT count(*) FROM outbox_plain_fallbacks",
	// core
	"chats":                "SELECT count(*) FROM chats",
	"messages":             "SELECT count(*) FROM messages",
	"registered_chats":     "SELECT count(*) FROM registered_chats",
	"sessions":             "SELECT count(*) FROM sessions",
	"router_state":         "SELECT count(*) FROM router_state",
	"logs":                 "SELECT count(*) FROM logs",
	"conversation_history": "SELECT count(*) FROM conversation_history",
	"namespace_ownership":  "SELECT count(*) FROM namespace_ownership",
	// turns and tasks
	"turns":              "SELECT count(*) FROM turns",
	"scheduled_tasks":    "SELECT count(*) FROM scheduled_tasks",
	"task_run_logs":      "SELECT count(*) FROM task_run_logs",
	"task_runs":          "SELECT count(*) FROM task_runs",
	"task_attempts":      "SELECT count(*) FROM task_attempts",
	"task_occurrences":   "SELECT count(*) FROM task_occurrences",
	"task_verifications": "SELECT count(*) FROM task_verifications",
	"ipc_task_requests":  "SELECT count(*) FROM ipc_task_requests",
	// memory
	"memories": "SELECT count(*) FROM memories",
	// delivery and outbox
	"turn_results":           "SELECT count(*) FROM turn_results",
	"turn_result_members":    "SELECT count(*) FROM turn_result_members",
	"conversation_sequences": "SELECT count(*) FROM conversation_sequences",
	"delivery_groups":        "SELECT count(*) FROM delivery_groups",
	"outbox_parts":           "SELECT count(*) FROM outbox_parts",
	"outbox_attempts":        "SELECT count(*) FROM outbox_attempts",
	"ipc_message_requests":   "SELECT count(*) FROM ipc_message_requests",
	// artifacts
	"artifact_reservations":  "SELECT count(*) FROM artifact_reservations",
	"artifacts":              "SELECT count(*) FROM artifacts",
	"artifact_refs":          "SELECT count(*) FROM artifact_refs",
	"generated_preparations": "SELECT count(*) FROM generated_preparations",
}
