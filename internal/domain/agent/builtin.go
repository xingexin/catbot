package agent

func ObjectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func BuiltinTools() []Tool {
	text := map[string]any{"type": "string"}
	taskReplyHint := " IDs in results are internal references for subsequent tool calls. In normal replies use task names, readable times and statuses; show IDs only if the user explicitly asks."
	stepSchema := ObjectSchema(map[string]any{"id": text, "kind": map[string]any{"type": "string", "enum": []string{"tool", "agent"}}, "tool": text, "arguments": map[string]any{"type": "object"}, "prompt": text, "delaySec": map[string]any{"type": "integer", "minimum": 0, "maximum": 2678400}}, "id", "kind")
	taskSchema := ObjectSchema(map[string]any{
		"name": text, "kind": map[string]any{"type": "string", "enum": []string{"once", "recurring", "manual"}},
		"cron": text, "timeZone": text, "runAt": text, "notify": map[string]any{"type": "boolean"},
		"notifyWhen": map[string]any{"type": "string", "description": "Optional condition referencing a BOOLEAN step output, e.g. ${steps.watch.changed}. Omit for unconditional reminders. Never reference reminder text here."},
		"notifyText": map[string]any{"type": "string", "description": "Optional notification body referencing a STRING step output, e.g. ${steps.reminder.text}."},
		"steps":      map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": stepSchema},
	}, "name", "kind", "steps")
	return []Tool{
		{Name: "system__task_create", Description: "Create a persistent task only when the current user explicitly requests a reminder, notification, timed execution or automation. Advice, plans and proposed schedules alone do not authorize creating reminders; ask first if intent is unclear. Report task status only from actual tool results. kind=once requires runAt RFC3339 with timezone; recurring requires cron and timeZone. Steps call tools or an agent. For a simple text reminder, use example__echo if available with arguments.text, notify=true, notifyText=${steps.reminder.text}, and OMIT notifyWhen. Otherwise use an agent step. notifyWhen is only for BOOLEAN conditions. For new email alerts use one mail__watch step, notifyWhen=${steps.watch.changed}, notifyText=${steps.watch.notificationText}; polling every 5 minutes uses cron= */5 * * * *. First check sets a baseline, no historical alerts." + taskReplyHint, InputSchema: taskSchema, RetrySafe: true},
		{Name: "system__task_list", Description: "List tasks and execution status." + taskReplyHint, InputSchema: ObjectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__task_update", Description: "Update an existing task, including rescheduling. Supply id and changed fields. No new duplicate task is created." + taskReplyHint, InputSchema: ObjectSchema(map[string]any{"id": text, "name": text, "cron": text, "timeZone": text, "runAt": text, "paused": map[string]any{"type": "boolean"}}, "id"), RetrySafe: true},
		{Name: "system__task_control", Description: "Pause, resume, cancel, or immediately trigger a task." + taskReplyHint, InputSchema: ObjectSchema(map[string]any{"id": text, "action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "cancel", "trigger"}}}, "id", "action"), RetrySafe: true},
		{Name: "system__artifact_list", Description: "List uploaded files and completed analysis artifacts.", InputSchema: ObjectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__artifact_read", Description: "Read a saved analysis result by artifact ID.", InputSchema: ObjectSchema(map[string]any{"id": text}, "id"), RetrySafe: true},
	}
}
