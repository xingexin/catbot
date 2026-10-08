package repository

import (
	"context"

	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Repository struct{ records store.Store }

// New reuses the existing task/execution JSONB records and locks;
// introducing this typed boundary does not migrate or rewrite stored data.
func New(records store.Store) *Repository {
	return &Repository{records: records}
}

func (r *Repository) Task(ctx context.Context, id string) (taskentity.Task, error) {
	var value taskentity.Task
	err := r.records.Get(ctx, "task", id, &value)
	return value, err
}
func (r *Repository) SaveTask(ctx context.Context, value taskentity.Task) error {
	return r.records.Put(ctx, "task", value.ID, value)
}
func (r *Repository) LockTask(ctx context.Context, id string) (func(), error) {
	return r.records.Lock(ctx, "task:"+id)
}
func (r *Repository) Execution(ctx context.Context, id string) (taskentity.TaskExecution, error) {
	var value taskentity.TaskExecution
	err := r.records.Get(ctx, "execution", id, &value)
	return value, err
}
func (r *Repository) SaveExecution(ctx context.Context, value taskentity.TaskExecution) error {
	return r.records.Put(ctx, "execution", value.ID, value)
}
func (r *Repository) Executions(ctx context.Context, taskID string) ([]taskentity.TaskExecution, error) {
	all, err := store.All[taskentity.TaskExecution](ctx, r.records, "execution")
	if err != nil {
		return nil, err
	}
	selected := make([]taskentity.TaskExecution, 0, len(all))
	for _, execution := range all {
		if execution.TaskID == taskID {
			selected = append(selected, execution)
		}
	}
	return selected, nil
}
