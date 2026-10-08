package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/plugin"
	"agentTest/internal/store"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const Queue = "secretary-tasks"

type Input struct {
	TaskID   string
	Revision int
	Manual   bool
}
type Snapshot struct {
	Task    domain.Task
	Config  domain.Config
	Persona domain.Persona
}
type StepInput struct {
	Snapshot    Snapshot
	Step        domain.Step
	ExecutionID string
	Results     map[string]any
}
type Host interface {
	Step(context.Context, StepInput) (any, error)
	Notify(context.Context, Snapshot, string, string) error
}
type Engine struct {
	TaskQueue string
	Client    client.Client
	Store     store.Store
	Host      Host
}

func (e *Engine) queue() string {
	if e.TaskQueue != "" {
		return e.TaskQueue
	}
	return Queue
}

func New(c client.Client, s store.Store, h Host) *Engine {
	return &Engine{Client: c, Store: s, Host: h}
}
func (e *Engine) Worker() worker.Worker {
	w := worker.New(e.Client, e.queue(), worker.Options{})
	w.RegisterWorkflow(TaskWorkflow)
	w.RegisterActivity(e.Begin)
	w.RegisterActivity(e.ExecuteStep)
	w.RegisterActivity(e.Finish)
	return w
}
func (e *Engine) Ping(ctx context.Context) error {
	_, err := e.Client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}
func onceID(t domain.Task) string { return fmt.Sprintf("task-%s-r%d", t.ID, t.Revision) }
func (e *Engine) Apply(ctx context.Context, t domain.Task, old *domain.Task) error {
	if old != nil && old.Kind == "once" {
		var active domain.TaskExecution
		keepActive := t.Paused && e.Store.Get(ctx, "execution", onceID(*old), &active) == nil && active.Status == "running"
		var err error
		if !keepActive {
			err = e.Client.CancelWorkflow(ctx, onceID(*old), "")
		}
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if old != nil && old.Kind == "recurring" && t.Kind != "recurring" {
		err := e.Client.ScheduleClient().GetHandle(ctx, t.ID).Delete(ctx)
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if t.Kind == "recurring" {
		spec := client.ScheduleSpec{CronExpressions: []string{t.Cron}, TimeZoneName: t.TimeZone}
		action := &client.ScheduleWorkflowAction{ID: "task-" + t.ID, Workflow: TaskWorkflow, Args: []any{Input{TaskID: t.ID, Revision: t.Revision}}, TaskQueue: e.queue()}
		handle := e.Client.ScheduleClient().GetHandle(ctx, t.ID)
		_, err := handle.Describe(ctx)
		if err == nil {
			return handle.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				s := in.Description.Schedule
				s.Spec = &spec
				s.Action = action
				s.State.Paused = t.Paused
				s.Policy.Overlap = enumspb.SCHEDULE_OVERLAP_POLICY_SKIP
				s.Policy.CatchupWindow = time.Duration(t.CatchupSec) * time.Second
				return &client.ScheduleUpdate{Schedule: &s}, nil
			}})
		}
		var missing *serviceerror.NotFound
		if !errors.As(err, &missing) {
			return err
		}
		_, err = e.Client.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: t.ID, Spec: spec, Action: action, Paused: t.Paused, Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: time.Duration(t.CatchupSec) * time.Second})
		return err
	}
	if t.Kind == "once" && !t.Paused {
		delay := time.Until(*t.RunAt)
		if delay < 0 {
			delay = 0
		}
		_, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: onceID(t), TaskQueue: e.queue(), StartDelay: delay, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, TaskWorkflow, Input{TaskID: t.ID, Revision: t.Revision})
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &started) {
			return nil
		}
		return err
	}
	return nil
}
func (e *Engine) Trigger(ctx context.Context, t domain.Task, operationID string) (string, error) {
	sum := sha256.Sum256([]byte(operationID))
	id := "manual-" + t.ID + "-" + hex.EncodeToString(sum[:12])
	_, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: e.queue(), WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, TaskWorkflow, Input{TaskID: t.ID, Revision: t.Revision, Manual: true})
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &started) {
		return id, nil
	}
	return id, err
}
func (e *Engine) Cancel(ctx context.Context, t domain.Task) error {
	if t.Kind == "recurring" {
		err := e.Client.ScheduleClient().GetHandle(ctx, t.ID).Delete(ctx)
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	if t.Kind == "once" {
		err := e.Client.CancelWorkflow(ctx, onceID(t), "")
		var missing *serviceerror.NotFound
		if err != nil && !errors.As(err, &missing) {
			return err
		}
	}
	executions, err := store.All[domain.TaskExecution](ctx, e.Store, "execution")
	if err != nil {
		return err
	}
	for _, x := range executions {
		if x.TaskID == t.ID && x.Status == "running" {
			if err := e.Client.CancelWorkflow(ctx, x.ID, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func TaskWorkflow(ctx workflow.Context, input Input) (result map[string]any, runErr error) {
	opts := workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3}}
	ctx = workflow.WithActivityOptions(ctx, opts)
	id := workflow.GetInfo(ctx).WorkflowExecution.ID
	var snapshot Snapshot
	if err := workflow.ExecuteActivity(ctx, "Begin", input, id).Get(ctx, &snapshot); err != nil {
		return nil, err
	}
	result = map[string]any{}
	defer func() {
		finishCtx, _ := workflow.NewDisconnectedContext(ctx)
		status := "completed"
		message := ""
		if runErr != nil {
			status = "failed"
			message = runErr.Error()
			if temporal.IsCanceledError(runErr) {
				status = "cancelled"
			}
		}
		err := workflow.ExecuteActivity(finishCtx, "Finish", snapshot, id, status, message, result).Get(finishCtx, nil)
		if runErr == nil && err != nil {
			runErr = err
		}
	}()
	for _, step := range snapshot.Task.Steps {
		if step.DelaySec > 0 {
			if err := workflow.NewTimer(ctx, time.Duration(step.DelaySec)*time.Second).Get(ctx, nil); err != nil {
				return result, err
			}
		}
		stepCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Hour, HeartbeatTimeout: 30 * time.Second, WaitForCancellation: true,
			RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 30 * time.Second, MaximumAttempts: 3},
		})
		var value any
		if err := workflow.ExecuteActivity(stepCtx, "ExecuteStep", StepInput{Snapshot: snapshot, Step: step, ExecutionID: id, Results: result}).Get(stepCtx, &value); err != nil {
			return result, err
		}
		result[step.ID] = value
	}
	return result, nil
}
func (e *Engine) Begin(ctx context.Context, in Input, id string) (Snapshot, error) {
	unlock, err := e.Store.Lock(ctx, "task:"+in.TaskID)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	var prior Snapshot
	if err := e.Store.Get(ctx, "execution-snapshot", id, &prior); err == nil {
		return prior, e.ensureExecution(ctx, prior, in, id)
	} else if !errors.Is(err, store.ErrNotFound) {
		return prior, err
	}
	var t domain.Task
	if err := e.Store.Get(ctx, "task", in.TaskID, &t); err != nil {
		return Snapshot{}, err
	}
	if t.Status == "provisioning" {
		return Snapshot{}, errors.New("task is still provisioning")
	}
	if t.Revision != in.Revision || t.Status == "cancelled" || (!in.Manual && t.Paused) {
		_ = e.Store.Put(ctx, "execution", id, domain.TaskExecution{ID: id, TaskID: t.ID, Status: "skipped", Error: "task superseded, paused or cancelled", StartedAt: time.Now().UTC()})
		return Snapshot{}, temporal.NewNonRetryableApplicationError("task superseded, paused or cancelled", "InactiveTask", nil)
	}
	if err := e.checkOverlap(ctx, t.ID, id); err != nil {
		return Snapshot{}, err
	}
	for pluginID := range t.Versions {
		var p domain.Plugin
		if err := e.Store.Get(ctx, "plugin", pluginID, &p); err != nil || !p.Enabled {
			_ = e.Store.Put(ctx, "execution", id, domain.TaskExecution{ID: id, TaskID: t.ID, Status: "skipped", Error: "plugin dependency unavailable: " + pluginID, StartedAt: time.Now().UTC()})
			return Snapshot{}, temporal.NewNonRetryableApplicationError("plugin dependency unavailable: "+pluginID, "PluginDisabled", err)
		}
		key, err := plugin.Pin(ctx, e.Store, p)
		if err != nil {
			return Snapshot{}, err
		}
		t.Versions[pluginID] = key
	}
	snap := Snapshot{Task: t}
	if err := e.Store.Get(ctx, "config", t.ConfigID, &snap.Config); err != nil {
		return snap, err
	}
	if err := e.Store.Get(ctx, "persona", t.PersonaID, &snap.Persona); err != nil {
		return snap, err
	}
	// A snapshot must exist before a running occupancy record. If this first
	// write fails through all Activity retries, later polls remain runnable.
	// Once saved, a retry uses that same snapshot and repairs the second write.
	if err := e.Store.Put(ctx, "execution-snapshot", id, snap); err != nil {
		return snap, err
	}
	return snap, e.ensureExecution(ctx, snap, in, id)
}

// Caller holds the task lock. Saving a snapshot is not a claim: a retry after a
// failed execution write must still check for a newer execution already begun.
func (e *Engine) ensureExecution(ctx context.Context, snap Snapshot, in Input, id string) error {
	var current domain.TaskExecution
	if err := e.Store.Get(ctx, "execution", id, &current); err == nil {
		if current.Status == "skipped" || current.Status == "interrupted" {
			return temporal.NewNonRetryableApplicationError("execution is no longer active", "InactiveTask", nil)
		}
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var task domain.Task
	if err := e.Store.Get(ctx, "task", snap.Task.ID, &task); err != nil {
		return err
	}
	if task.Revision != in.Revision || task.Status == "cancelled" || (!in.Manual && task.Paused) {
		if err := e.Store.Put(ctx, "execution", id, domain.TaskExecution{ID: id, TaskID: task.ID, Status: "skipped", Error: "task superseded, paused or cancelled before execution began", StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return temporal.NewNonRetryableApplicationError("task superseded, paused or cancelled", "InactiveTask", nil)
	}
	if err := e.checkOverlap(ctx, snap.Task.ID, id); err != nil {
		return err
	}
	x := domain.TaskExecution{ID: id, TaskID: snap.Task.ID, Status: "running", Results: map[string]any{}, StartedAt: time.Now().UTC(), Config: &snap.Config, Persona: &snap.Persona, Versions: snap.Task.Versions}
	return e.Store.Put(ctx, "execution", id, x)
}

func (e *Engine) checkOverlap(ctx context.Context, taskID, id string) error {
	existing, err := store.All[domain.TaskExecution](ctx, e.Store, "execution")
	if err != nil {
		return err
	}
	for _, x := range existing {
		if x.TaskID != taskID || x.ID == id || x.Status != "running" {
			continue
		}
		closed := false
		if e.Client != nil {
			// A failed Begin/Finish may have left an old running row behind.
			// Temporal, rather than elapsed wall time, decides whether it is
			// safe to free that occupancy. Never replay the previous actions.
			description, err := e.Client.DescribeWorkflowExecution(ctx, x.ID, "")
			var missing *serviceerror.NotFound
			if errors.As(err, &missing) {
				closed = true
			} else if err != nil {
				return fmt.Errorf("verify active task execution: %w", err)
			} else if description == nil || description.WorkflowExecutionInfo == nil {
				return errors.New("Temporal returned no task execution status")
			} else {
				switch description.WorkflowExecutionInfo.Status {
				case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
					enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
					enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
					closed = true
				case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
				default:
					return errors.New("Temporal task execution status is unknown")
				}
			}
		}
		if closed {
			now := time.Now().UTC()
			x.Status, x.Error, x.FinishedAt = "interrupted", "workflow is no longer active; inspect saved results and delivery state before retrying", &now
			if err := e.Store.Put(ctx, "execution", x.ID, x); err != nil {
				return err
			}
			continue
		}
		if err := e.Store.Put(ctx, "execution", id, domain.TaskExecution{ID: id, TaskID: taskID, Status: "skipped", Error: "another execution is active", StartedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return temporal.NewNonRetryableApplicationError("another execution is active", "OverlapSkipped", nil)
	}
	return nil
}
func (e *Engine) ExecuteStep(ctx context.Context, in StepInput) (any, error) {
	resultID := in.ExecutionID + ":" + in.Step.ID
	var prior any
	if err := e.Store.Get(ctx, "step-result", resultID, &prior); err == nil {
		return map[string]any{"_resultRef": resultID}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	hydrated, err := e.hydrate(ctx, in.Results)
	if err != nil {
		return nil, err
	}
	in.Results = hydrated
	done := make(chan struct{})
	defer close(done)
	go func() {
		timer := time.NewTicker(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				activity.RecordHeartbeat(ctx, in.Step.ID)
			}
		}
	}()
	activity.RecordHeartbeat(ctx, in.Step.ID)
	value, err := e.Host.Step(ctx, in)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, temporal.NewCanceledError("step cancelled")
		}
		if in.Step.Kind == "tool" {
			parts := strings.SplitN(in.Step.Tool, "__", 2)
			if len(parts) == 2 {
				var p domain.Plugin
				if e.Store.Get(ctx, "plugin-version", in.Snapshot.Task.Versions[parts[0]], &p) == nil {
					for _, spec := range p.Manifest.Tools {
						if spec.Name == parts[1] && spec.RetrySafe {
							return nil, err
						}
					}
				}
			}
		}
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "StepFailed", err)
	}
	if err := e.Store.Put(ctx, "step-result", resultID, value); err != nil {
		return nil, err
	}
	var x domain.TaskExecution
	if err := e.Store.Get(ctx, "execution", in.ExecutionID, &x); err != nil {
		return nil, err
	}
	if x.Results == nil {
		x.Results = map[string]any{}
	}
	x.Results[in.Step.ID] = value
	if err := e.Store.Put(ctx, "execution", x.ID, x); err != nil {
		return nil, err
	}
	return map[string]any{"_resultRef": resultID}, nil
}
func (e *Engine) hydrate(ctx context.Context, results map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for key, value := range results {
		if obj, ok := value.(map[string]any); ok {
			if ref, ok := obj["_resultRef"].(string); ok {
				var full any
				if err := e.Store.Get(ctx, "step-result", ref, &full); err != nil {
					return nil, err
				}
				out[key] = full
				continue
			}
		}
		out[key] = value
	}
	return out, nil
}
func (e *Engine) Finish(ctx context.Context, snapshot Snapshot, id, status, message string, result map[string]any) error {
	var x domain.TaskExecution
	if err := e.Store.Get(ctx, "execution", id, &x); err != nil {
		return err
	}
	// A retry may only need to repair the task status after the execution was
	// saved. Do not reset a recorded notification failure or attempt it again.
	if x.Status == "notification_failed" {
		return e.finishOnceTask(ctx, snapshot.Task, x)
	}
	full, err := e.hydrate(ctx, result)
	if err != nil {
		return err
	}
	result = full
	x.Status = status
	x.Error = message
	x.Results = result
	now := time.Now().UTC()
	x.FinishedAt = &now
	if err := e.Store.Put(ctx, "execution", id, x); err != nil {
		return err
	}
	if snapshot.Task.Notify {
		text, send, notificationErr := Notification(snapshot.Task, status, message, result)
		if notificationErr == nil {
			allowed, policyErr := e.allowNotification(ctx, snapshot.Task, id, status)
			if policyErr != nil {
				return policyErr
			}
			if send && allowed {
				notificationErr = e.Host.Notify(ctx, snapshot, text, "task-notify:"+id)
			}
		}
		if notificationErr != nil {
			x.Status = "notification_failed"
			x.Error = notificationErr.Error()
			if err := e.Store.Put(ctx, "execution", id, x); err != nil {
				return err
			}
		}
	}
	return e.finishOnceTask(ctx, snapshot.Task, x)
}

func (e *Engine) finishOnceTask(ctx context.Context, snapshot domain.Task, x domain.TaskExecution) error {
	if snapshot.Kind != "once" {
		return nil
	}
	unlock, err := e.Store.Lock(ctx, "task:"+snapshot.ID)
	if err != nil {
		return err
	}
	defer unlock()
	var current domain.Task
	if err := e.Store.Get(ctx, "task", snapshot.ID, &current); err != nil {
		return err
	}
	if current.Revision != snapshot.Revision || current.Status == "cancelled" {
		return nil
	}
	current.Status = x.Status
	current.Error = x.Error
	return e.Store.Put(ctx, "task", current.ID, current)
}

// Notification uses human-readable results or a brief completion notice.
// Structured results remain in the execution record. Conditions never hide failures.
func Notification(t domain.Task, status, failure string, results map[string]any) (string, bool, error) {
	if status != "completed" {
		return t.Name + "：" + status + "\n" + failure, true, nil
	}
	if t.NotifyWhen != "" {
		v, err := Resolve(t.NotifyWhen, results)
		if err != nil {
			return "", false, err
		}
		changed, ok := v.(bool)
		if !ok {
			return "", false, errors.New("notification condition must resolve to a boolean")
		}
		if !changed {
			return "", false, nil
		}
	}
	if t.NotifyText != "" {
		v, err := Resolve(t.NotifyText, results)
		if err != nil {
			return "", false, err
		}
		text, ok := v.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", false, errors.New("notification text must resolve to nonempty text")
		}
		return text, true, nil
	}
	if len(t.Steps) > 0 {
		last, _ := results[t.Steps[len(t.Steps)-1].ID].(map[string]any)
		for _, key := range []string{"notificationText", "text"} {
			if text, ok := last[key].(string); ok && strings.TrimSpace(text) != "" {
				return text, true, nil
			}
		}
	}
	return t.Name + "已完成，详细结果可在管理端查看。", true, nil
}

var reference = regexp.MustCompile(`^\$\{steps\.([a-zA-Z0-9_-]+)(?:\.([^}]+))?\}$`)

// ReferenceStep validates a single typed result reference, never a template.
func ReferenceStep(expression string) (string, error) {
	match := reference.FindStringSubmatch(expression)
	if match == nil || strings.HasSuffix(expression, ".}") {
		return "", errors.New("notification must be a single step result reference")
	}
	return match[1], nil
}

// Resolve supports typed references to earlier step output fields.
func Resolve(value any, results map[string]any) (any, error) {
	switch x := value.(type) {
	case string:
		m := reference.FindStringSubmatch(x)
		if m == nil {
			return x, nil
		}
		cur, ok := results[m[1]]
		if !ok {
			return nil, fmt.Errorf("step reference not found: %s", m[1])
		}
		if m[2] != "" {
			for _, key := range strings.Split(m[2], ".") {
				obj, ok := cur.(map[string]any)
				if !ok {
					return nil, errors.New("step reference is not an object")
				}
				cur, ok = obj[key]
				if !ok {
					return nil, fmt.Errorf("step reference field not found: %s", key)
				}
			}
		}
		return cur, nil
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			r, err := Resolve(v, results)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := []any{}
		for _, v := range x {
			r, err := Resolve(v, results)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	default:
		return value, nil
	}
}
