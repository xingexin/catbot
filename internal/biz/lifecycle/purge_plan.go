package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type PurgeTarget struct {
	Resource domain.Resource `json:"resource"`
	ID       string          `json:"id"`
}

type PurgeItem struct {
	Resource domain.Resource `json:"resource"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Selected bool            `json:"selected"`
	Archived bool            `json:"archived"`
}

type PurgeBlocker struct {
	Resource domain.Resource `json:"resource"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Reason   string          `json:"reason"`
}

type PurgeRequest struct {
	Items []PurgeTarget `json:"items"`
	Token string        `json:"token,omitempty"`
}

type PurgePlan struct {
	Token    string         `json:"token"`
	Items    []PurgeItem    `json:"items"`
	Blockers []PurgeBlocker `json:"blockers"`
}

// PreviewPurge reads a stable reference graph; it never archives, cancels, or
// deletes anything. Executors must revalidate this plan before applying it.
func (s *Service) PreviewPurge(ctx context.Context, in PurgeRequest) (PurgePlan, error) {
	unlock, err := s.Store.Lock(ctx, domain.ReferenceLock)
	if err != nil {
		return PurgePlan{}, err
	}
	defer unlock()
	return s.previewPurgeLocked(ctx, in)
}

type purgeNode struct {
	target  PurgeTarget
	record  map[string]any
	archive *domain.ArchiveRecord
	extra   map[string]any
	missing bool
}

type purgeGraph struct {
	nodes          map[PurgeTarget]*purgeNode
	dependents     map[PurgeTarget]map[PurgeTarget]bool
	privateSecrets map[PurgeTarget][]PurgeTarget
}

func (s *Service) previewPurgeLocked(ctx context.Context, in PurgeRequest) (PurgePlan, error) {
	plan := PurgePlan{Items: []PurgeItem{}, Blockers: []PurgeBlocker{}}
	if len(in.Items) == 0 || len(in.Items) > 100 {
		return plan, errors.New("每批请选择 1 至 100 项")
	}
	selected := map[PurgeTarget]bool{}
	for _, target := range in.Items {
		if !target.Resource.Valid() || strings.TrimSpace(target.ID) == "" || len(target.ID) > 512 || strings.ContainsRune(target.ID, '\x00') {
			return plan, errors.New("无效的永久删除对象")
		}
		selected[target] = true
	}
	graph, err := s.loadPurgeGraph(ctx)
	if err != nil {
		return plan, err
	}
	for target := range selected {
		node := graph.nodes[target]
		if node == nil || node.missing {
			return plan, fmt.Errorf("所选记录不存在: %w", store.ErrNotFound)
		}
		if node.archive == nil {
			return plan, errors.New("只能永久删除已归档的记录")
		}
	}
	closure := map[PurgeTarget]bool{}
	pending := sortedPurgeTargets(selected)
	for {
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			target := pending[0]
			pending = pending[1:]
			if closure[target] {
				continue
			}
			closure[target] = true
			for child := range graph.dependents[target] {
				pending = append(pending, child)
			}
		}
		// A private key can become exclusively owned after other approved
		// records are removed. Include that possibility in confirmation, but
		// never expand the plan into records sharing a credential externally.
		for owner := range closure {
			for _, secret := range graph.privateSecrets[owner] {
				if closure[secret] {
					continue
				}
				sharedOutside := false
				for other, candidate := range graph.nodes {
					if other != owner && other != secret && !closure[other] && privateSecretUsedElsewhere(candidate, secret.ID) {
						sharedOutside = true
						break
					}
				}
				if !sharedOutside {
					pending = append(pending, secret)
				}
			}
		}
		if len(pending) == 0 {
			break
		}
	}
	order, cycles := graph.order(closure)
	for _, target := range order {
		node := graph.nodes[target]
		name := graph.itemName(node)
		item := PurgeItem{Resource: target.Resource, ID: target.ID, Name: name, Selected: selected[target], Archived: node.archive != nil}
		plan.Items = append(plan.Items, item)
		reason, err := s.purgePlanBlocker(ctx, node)
		if err != nil {
			return plan, err
		}
		if cycles[target] {
			reason = "关联记录存在循环引用，需先解除循环绑定后再永久删除"
		}
		if reason != "" {
			plan.Blockers = append(plan.Blockers, PurgeBlocker{Resource: target.Resource, ID: target.ID, Name: name, Reason: reason})
		}
	}
	if err := s.loadOwnedPurgeData(ctx, graph, closure); err != nil {
		return plan, err
	}
	type digestItem struct {
		Target   PurgeTarget
		Selected bool
		Record   map[string]any
		Archive  *domain.ArchiveRecord
		Extra    map[string]any
	}
	digest := struct {
		Items    []digestItem
		Blockers []PurgeBlocker
	}{Blockers: plan.Blockers}
	for _, target := range sortedPurgeTargets(closure) {
		node := graph.nodes[target]
		digest.Items = append(digest.Items, digestItem{Target: target, Selected: selected[target], Record: node.record, Archive: node.archive, Extra: node.extra})
	}
	raw, err := json.Marshal(digest)
	if err != nil {
		return plan, err
	}
	sum := sha256.Sum256(raw)
	plan.Token = hex.EncodeToString(sum[:])
	return plan, nil
}

// itemName is presentation-only: storage identifiers remain in the separate ID
// field and archive naming stays unchanged.
func (g *purgeGraph) itemName(node *purgeNode) string {
	for _, field := range []string{"name", "title"} {
		if value := planString(node.record, field); value != "" {
			return value
		}
	}
	if node.target.Resource == domain.ResourcePlugin {
		if name := planString(planObject(node.record, "manifest"), "name"); name != "" {
			return name
		}
	}
	labels := map[domain.Resource]string{
		domain.ResourceSession: "对话", domain.ResourceTask: "任务", domain.ResourceArtifact: "文件或结果",
		domain.ResourcePersona: "人格", domain.ResourcePlugin: "插件", domain.ResourceConfig: "模型配置",
		domain.ResourceRun: "对话运行", domain.ResourceExecution: "执行记录", domain.ResourceNotification: "通知",
		domain.ResourceDelivery: "投递记录", domain.ResourceModelCall: "模型调用", domain.ResourceSecret: "凭证",
	}
	label := labels[node.target.Resource]
	relatedName := func(resource domain.Resource, id string) string {
		related := g.nodes[PurgeTarget{Resource: resource, ID: id}]
		if related == nil {
			return ""
		}
		for _, field := range []string{"name", "title"} {
			if name := planString(related.record, field); name != "" {
				return name
			}
		}
		if resource == domain.ResourcePlugin {
			return planString(planObject(related.record, "manifest"), "name")
		}
		return ""
	}
	ownerName := ""
	switch node.target.Resource {
	case domain.ResourceRun, domain.ResourceDelivery:
		ownerName = relatedName(domain.ResourceSession, planString(node.record, "sessionId"))
	case domain.ResourceExecution, domain.ResourceNotification:
		ownerName = relatedName(domain.ResourceTask, planString(node.record, "taskId"))
		sessionID := planString(node.record, "sessionId")
		if ownerName == "" && node.target.Resource == domain.ResourceExecution {
			snapshot, _ := node.extra["execution-snapshot:"+node.target.ID].(map[string]any)
			task := planObject(snapshot, "task")
			ownerName = relatedName(domain.ResourceTask, planString(task, "id"))
			if ownerName == "" {
				ownerName = planString(task, "name")
			}
			if sessionID == "" {
				sessionID = planString(task, "sessionId")
			}
		}
		if ownerName == "" {
			ownerName = relatedName(domain.ResourceSession, sessionID)
		}
	case domain.ResourceModelCall:
		ownerName = relatedName(domain.ResourcePlugin, planString(node.record, "pluginId"))
	}
	name := label
	if ownerName != "" {
		name = ownerName + " · " + label
	} else {
		shortID := []rune(node.target.ID)
		if len(shortID) > 8 {
			shortID = append([]rune("…"), shortID[len(shortID)-8:]...)
		}
		name += " · " + string(shortID)
	}
	for _, field := range []string{"startedAt", "createdAt"} {
		if value, err := time.Parse(time.RFC3339Nano, planString(node.record, field)); err == nil {
			return name + " · " + value.UTC().Format("2006-01-02 15:04:05 UTC")
		}
	}
	return name
}

func (s *Service) loadPurgeGraph(ctx context.Context) (*purgeGraph, error) {
	graph := &purgeGraph{nodes: map[PurgeTarget]*purgeNode{}, dependents: map[PurgeTarget]map[PurgeTarget]bool{}, privateSecrets: map[PurgeTarget][]PurgeTarget{}}
	for resource := domain.ResourceSession; resource <= domain.ResourceSecret; resource++ {
		rows, err := planRecords(ctx, s.Store, resource.StorageKind())
		if err != nil {
			return nil, err
		}
		for id, record := range rows {
			target := PurgeTarget{Resource: resource, ID: id}
			graph.nodes[target] = &purgeNode{target: target, record: record, extra: map[string]any{}}
		}
	}
	archives, err := repository.List(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	for _, archive := range archives {
		if node := graph.nodes[PurgeTarget{Resource: archive.Resource, ID: archive.RecordID}]; node != nil {
			node.archive = &archive
		}
	}
	versions, err := planRecords(ctx, s.Store, "plugin-version")
	if err != nil {
		return nil, err
	}
	for id, record := range versions {
		graph.extraNode(domain.ResourcePlugin, planString(record, "id")).extra["plugin-version:"+id] = record
	}
	snapshots, err := planRecords(ctx, s.Store, "execution-snapshot")
	if err != nil {
		return nil, err
	}
	for id, record := range snapshots {
		graph.extraNode(domain.ResourceExecution, id).extra["execution-snapshot:"+id] = record
	}
	for parent, node := range graph.nodes {
		for child, dependent := range graph.nodes {
			if parent == child {
				continue
			}
			if purgeDependency(parent, node, dependent) {
				if graph.dependents[parent] == nil {
					graph.dependents[parent] = map[PurgeTarget]bool{}
				}
				graph.dependents[parent][child] = true
			}
		}
		if parent.Resource == domain.ResourcePlugin {
			for secret, value := range graph.nodes {
				if secret.Resource != domain.ResourceSecret || !strings.HasPrefix(secret.ID, "plugin-"+parent.ID+"-") || !nodeUsesSecret(node, secret.ID) {
					continue
				}
				if !value.missing {
					graph.privateSecrets[parent] = append(graph.privateSecrets[parent], secret)
				}
			}
		}
	}
	return graph, nil
}

func (g *purgeGraph) extraNode(resource domain.Resource, id string) *purgeNode {
	target := PurgeTarget{Resource: resource, ID: id}
	if node := g.nodes[target]; node != nil {
		return node
	}
	node := &purgeNode{target: target, record: map[string]any{}, extra: map[string]any{}, missing: true}
	g.nodes[target] = node
	return node
}

// Dependencies are inferred from reference fields, never a matching name,
// prompt, message body, or arbitrary string containing the selected ID.
func purgeDependency(parent PurgeTarget, parentNode, child *purgeNode) bool {
	record := child.record
	resource := child.target.Resource
	id := parent.ID
	switch parent.Resource {
	case domain.ResourceConfig, domain.ResourcePersona:
		field, snapshot := "configId", "config"
		if parent.Resource == domain.ResourcePersona {
			field, snapshot = "personaId", "persona"
		}
		if (resource == domain.ResourceSession || resource == domain.ResourceTask || resource == domain.ResourceModelCall) && planString(record, field) == id {
			return true
		}
		if (resource == domain.ResourceRun || resource == domain.ResourceExecution) && planString(planObject(record, snapshot), "id") == id {
			return true
		}
		if resource == domain.ResourcePlugin && configuredReference(planObject(record, "config"), field, id) {
			return true
		}
		for _, extra := range child.extra {
			value, _ := extra.(map[string]any)
			if resource == domain.ResourcePlugin && configuredReference(planObject(value, "config"), field, id) {
				return true
			}
			if resource == domain.ResourceExecution && (planString(planObject(value, snapshot), "id") == id || planString(planObject(value, "task"), field) == id) {
				return true
			}
		}
	case domain.ResourceSession:
		return (resource == domain.ResourceSession && planString(record, "originSessionId") == id) ||
			((resource == domain.ResourceTask || resource == domain.ResourceRun || resource == domain.ResourceNotification || resource == domain.ResourceDelivery) && planString(record, "sessionId") == id)
	case domain.ResourceTask:
		if (resource == domain.ResourceExecution || resource == domain.ResourceNotification) && planString(record, "taskId") == id {
			return true
		}
		if resource == domain.ResourceExecution {
			for _, extra := range child.extra {
				value, _ := extra.(map[string]any)
				if planString(planObject(value, "task"), "id") == id {
					return true
				}
			}
		}
	case domain.ResourcePlugin:
		if (resource == domain.ResourceTask || resource == domain.ResourceRun || resource == domain.ResourceExecution) && pluginReference(record, id) {
			return true
		}
		if (resource == domain.ResourceModelCall || resource == domain.ResourceNotification) && planString(record, "pluginId") == id {
			return true
		}
		if resource == domain.ResourceExecution {
			for _, extra := range child.extra {
				value, _ := extra.(map[string]any)
				if pluginReference(planObject(value, "task"), id) {
					return true
				}
			}
		}
	case domain.ResourceSecret:
		return nodeUsesSecret(child, id)
	case domain.ResourceExecution:
		if resource == domain.ResourceSession && child.target.ID == "task-session-"+id {
			return true
		}
		if resource == domain.ResourceNotification && (planString(record, "operationId") == "task-notify:"+id || child.target.ID == "notification-task-notify:"+id) {
			return true
		}
		return resource == domain.ResourceDelivery && child.target.ID == "task-notify:"+id
	case domain.ResourceRun:
		if resource == domain.ResourceNotification && (child.target.ID == "notification-reply:"+id || planString(record, "operationId") == "reply:"+id) {
			return true
		}
		return resource == domain.ResourceDelivery && child.target.ID == "reply:"+id
	case domain.ResourceNotification:
		return resource == domain.ResourceDelivery && child.target.ID == planString(parentNode.record, "operationId")
	case domain.ResourceArtifact:
		if resource == domain.ResourceTask || resource == domain.ResourceSession || resource == domain.ResourceRun || resource == domain.ResourceExecution || resource == domain.ResourceArtifact {
			if artifactReference(record, id) {
				return true
			}
			for _, extra := range child.extra {
				if artifactReference(extra, id) {
					return true
				}
			}
		}
	}
	return false
}

func nodeUsesSecret(node *purgeNode, id string) bool {
	switch node.target.Resource {
	case domain.ResourceConfig:
		return planString(node.record, "credentialId") == id
	case domain.ResourcePlugin:
		if secretReference(planObject(node.record, "secrets"), id) {
			return true
		}
		for _, extra := range node.extra {
			value, _ := extra.(map[string]any)
			if secretReference(planObject(value, "secrets"), id) {
				return true
			}
		}
	case domain.ResourceRun, domain.ResourceExecution:
		if planString(planObject(node.record, "config"), "credentialId") == id {
			return true
		}
		for _, extra := range node.extra {
			value, _ := extra.(map[string]any)
			if planString(planObject(value, "config"), "credentialId") == id {
				return true
			}
		}
	}
	return false
}

// Keep the conservative private-credential ownership rule used by the plugin
// purge handler; an ambiguous external mention prevents automatic inclusion.
func privateSecretUsedElsewhere(node *purgeNode, id string) bool {
	switch node.target.Resource {
	case domain.ResourceConfig, domain.ResourcePlugin, domain.ResourceRun, domain.ResourceExecution:
		if exactSecretValue(node.record, id) {
			return true
		}
		for _, value := range node.extra {
			if exactSecretValue(value, id) {
				return true
			}
		}
	}
	return false
}
func exactSecretValue(value any, id string) bool {
	switch current := value.(type) {
	case string:
		return current == id
	case map[string]any:
		for _, nested := range current {
			if exactSecretValue(nested, id) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if exactSecretValue(nested, id) {
				return true
			}
		}
	}
	return false
}

func secretReference(values map[string]any, id string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && text == id {
			return true
		}
	}
	return false
}
func configuredReference(value any, field, id string) bool {
	switch values := value.(type) {
	case map[string]any:
		suffix := strings.ToUpper(field[:1]) + field[1:]
		for key, nested := range values {
			if (key == field || strings.HasSuffix(key, suffix)) && nested == id {
				return true
			}
			if configuredReference(nested, field, id) {
				return true
			}
		}
	case []any:
		for _, nested := range values {
			if configuredReference(nested, field, id) {
				return true
			}
		}
	}
	return false
}
func pluginReference(record map[string]any, id string) bool {
	if _, ok := planObject(record, "versions")[id]; ok {
		return true
	}
	steps, _ := record["steps"].([]any)
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		if strings.HasPrefix(planString(step, "tool"), id+"__") {
			return true
		}
	}
	return false
}
func artifactReference(value any, id string) bool {
	switch values := value.(type) {
	case map[string]any:
		for key, nested := range values {
			switch key {
			case "artifactId", "sourceArtifactId", "fileId", "artifactIds", "fileIds":
				if references(nested, id) {
					return true
				}
			}
			if artifactReference(nested, id) {
				return true
			}
		}
	case []any:
		for _, nested := range values {
			if artifactReference(nested, id) {
				return true
			}
		}
	}
	return false
}

func (s *Service) purgePlanBlocker(ctx context.Context, node *purgeNode) (string, error) {
	if node.missing {
		return "存在尚未恢复的执行快照或插件版本，其所属记录缺失，请先修复记录", nil
	}
	target, record := node.target, node.record
	switch target.Resource {
	case domain.ResourceTask:
		if s.Handlers[target.Resource] == nil {
			return "任务生命周期服务未配置，无法确认停止调度", nil
		}
		if taskentity.ParseState(planString(record, "status")) == taskentity.StateUnknown {
			return "任务状态未知，请先修复后再删除", nil
		}
	case domain.ResourcePlugin:
		if s.Handlers[target.Resource] == nil {
			return "插件生命周期服务未配置，无法确认停止插件", nil
		}
	case domain.ResourceConfig, domain.ResourcePersona:
		if target.Resource == domain.ResourcePersona {
			if value, _ := record["default"].(bool); value {
				return "默认人格不能永久删除，请先设置其他默认人格", nil
			}
		}
		if s.CheckBindings != nil {
			if err := s.CheckBindings(ctx, target.Resource.StorageKind(), target.ID); err != nil {
				return err.Error(), nil
			}
		}
	case domain.ResourceRun, domain.ResourceExecution, domain.ResourceNotification, domain.ResourceDelivery, domain.ResourceModelCall:
		if domain.ParseActivityState(planString(record, "status")) == domain.ActivityUncertain {
			return "外部操作结果不明，请先核对处理结果后再删除", nil
		}
		if err := checkSettled(record); err != nil {
			return err.Error(), nil
		}
	case domain.ResourceArtifact:
		// Match the existing file deletion guard: a running request may still
		// hold an unpersisted attachment reference.
		for _, kind := range []string{"run", "execution"} {
			records, err := store.All[map[string]any](ctx, s.Store, kind)
			if err != nil {
				return "", err
			}
			for _, record := range records {
				if domain.ParseActivityState(planString(record, "status")).Mutable() {
					return "仍有正在执行的请求，请结束后再永久删除文件", nil
				}
			}
		}
	}
	return "", nil
}

type purgeVisit int

const (
	purgeUnvisited purgeVisit = 0
	purgeVisiting  purgeVisit = 1
	purgeVisited   purgeVisit = 2
)

func (g *purgeGraph) order(closure map[PurgeTarget]bool) ([]PurgeTarget, map[PurgeTarget]bool) {
	state := map[PurgeTarget]purgeVisit{}
	cycles := map[PurgeTarget]bool{}
	result := []PurgeTarget{}
	stack := []PurgeTarget{}
	var visit func(PurgeTarget)
	visit = func(target PurgeTarget) {
		if state[target] == purgeVisited {
			return
		}
		if state[target] == purgeVisiting {
			for i, ancestor := range stack {
				if ancestor == target {
					for _, member := range stack[i:] {
						cycles[member] = true
					}
					break
				}
			}
			return
		}
		state[target] = purgeVisiting
		stack = append(stack, target)
		for _, child := range sortedPurgeTargets(g.dependents[target]) {
			if closure[child] {
				visit(child)
			}
		}
		stack = stack[:len(stack)-1]
		state[target] = purgeVisited
		result = append(result, target)
	}
	for _, target := range sortedPurgeTargets(closure) {
		visit(target)
	}
	return result, cycles
}
func sortedPurgeTargets(values map[PurgeTarget]bool) []PurgeTarget {
	targets := make([]PurgeTarget, 0, len(values))
	for target := range values {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Resource != targets[j].Resource {
			return targets[i].Resource < targets[j].Resource
		}
		return targets[i].ID < targets[j].ID
	})
	return targets
}
func planString(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}
func planObject(record map[string]any, key string) map[string]any {
	value, _ := record[key].(map[string]any)
	if value == nil && key != "" {
		value, _ = record[strings.ToUpper(key[:1])+key[1:]].(map[string]any)
	}
	return value
}
func planRecords(ctx context.Context, s store.Store, kind string) (map[string]map[string]any, error) {
	refs, err := store.RecordRefs(ctx, s, kind)
	if err != nil {
		return nil, err
	}
	result := map[string]map[string]any{}
	for _, ref := range refs {
		var record map[string]any
		if err := s.Get(ctx, kind, ref.ID, &record); err != nil {
			return nil, err
		}
		result[ref.ID] = record
	}
	return result, nil
}

// Fingerprint owned records that are erased by existing purge handlers as part
// of an item: events, receipts, step results, plugin data and deduplication data.
func (s *Service) loadOwnedPurgeData(ctx context.Context, graph *purgeGraph, closure map[PurgeTarget]bool) error {
	load := func(node *purgeNode, kind, id string) error {
		var value any
		err := s.Store.Get(ctx, kind, id, &value)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		node.extra[kind+":"+id] = value
		return nil
	}
	base := []store.RecordRef{}
	for _, target := range sortedPurgeTargets(closure) {
		node := graph.nodes[target]
		base = append(base, store.RecordRef{Kind: target.Resource.StorageKind(), ID: target.ID})
		switch target.Resource {
		case domain.ResourceRun:
			if err := load(node, "qq-receipt", target.ID); err != nil {
				return err
			}
			var after int64
			events := []store.EventRecord{}
			for {
				batch, err := s.Store.Events(ctx, target.ID, after)
				if err != nil {
					return err
				}
				events = append(events, batch...)
				if len(batch) < 500 {
					break
				}
				next := batch[len(batch)-1].Sequence
				if next <= after {
					return errors.New("运行事件游标未推进")
				}
				after = next
			}
			node.extra["events"] = events
		case domain.ResourceExecution:
			stepIDs := map[string]bool{}
			for key := range planObject(node.record, "results") {
				stepIDs[key] = true
			}
			for key, raw := range node.extra {
				if !strings.HasPrefix(key, "execution-snapshot:") {
					continue
				}
				snapshot, _ := raw.(map[string]any)
				steps, _ := planObject(snapshot, "task")["steps"].([]any)
				for _, rawStep := range steps {
					step, _ := rawStep.(map[string]any)
					stepIDs[planString(step, "id")] = true
				}
			}
			for id := range stepIDs {
				if err := load(node, "step-result", target.ID+":"+id); err != nil {
					return err
				}
			}
		case domain.ResourcePlugin:
			refs, err := store.RecordRefs(ctx, s.Store, "plugin-data:"+target.ID)
			if err != nil {
				return err
			}
			for _, ref := range refs {
				if err := load(node, ref.Kind, ref.ID); err != nil {
					return err
				}
			}
		case domain.ResourceTask:
			for _, kind := range []string{"schedule-intent", "notification-incident"} {
				if err := load(node, kind, target.ID); err != nil {
					return err
				}
			}
		}
	}
	owned, err := CachePurgeRefs(ctx, s.Store, base)
	if err != nil {
		return err
	}
	// The full owned set affects confirmation, independently of which owner is
	// visited first. Use a deterministic node for its digest attachment.
	targets := sortedPurgeTargets(closure)
	if len(targets) > 0 {
		for _, ref := range owned {
			if err := load(graph.nodes[targets[0]], ref.Kind, ref.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
