// Code generated by ent, DO NOT EDIT.

package ent

import (
	"context"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqlgraph"
	"entgo.io/ent/schema/field"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/predicate"
	"github.com/guacsec/guac/pkg/assembler/backends/ent/sourcename"
)

// SourceNameDelete is the builder for deleting a SourceName entity.
type SourceNameDelete struct {
	config
	hooks    []Hook
	mutation *SourceNameMutation
}

// Where appends a list predicates to the SourceNameDelete builder.
func (snd *SourceNameDelete) Where(ps ...predicate.SourceName) *SourceNameDelete {
	snd.mutation.Where(ps...)
	return snd
}

// Exec executes the deletion query and returns how many vertices were deleted.
func (snd *SourceNameDelete) Exec(ctx context.Context) (int, error) {
	return withHooks(ctx, snd.sqlExec, snd.mutation, snd.hooks)
}

// ExecX is like Exec, but panics if an error occurs.
func (snd *SourceNameDelete) ExecX(ctx context.Context) int {
	n, err := snd.Exec(ctx)
	if err != nil {
		panic(err)
	}
	return n
}

func (snd *SourceNameDelete) sqlExec(ctx context.Context) (int, error) {
	_spec := sqlgraph.NewDeleteSpec(sourcename.Table, sqlgraph.NewFieldSpec(sourcename.FieldID, field.TypeInt))
	if ps := snd.mutation.predicates; len(ps) > 0 {
		_spec.Predicate = func(selector *sql.Selector) {
			for i := range ps {
				ps[i](selector)
			}
		}
	}
	affected, err := sqlgraph.DeleteNodes(ctx, snd.driver, _spec)
	if err != nil && sqlgraph.IsConstraintError(err) {
		err = &ConstraintError{msg: err.Error(), wrap: err}
	}
	snd.mutation.done = true
	return affected, err
}

// SourceNameDeleteOne is the builder for deleting a single SourceName entity.
type SourceNameDeleteOne struct {
	snd *SourceNameDelete
}

// Where appends a list predicates to the SourceNameDelete builder.
func (sndo *SourceNameDeleteOne) Where(ps ...predicate.SourceName) *SourceNameDeleteOne {
	sndo.snd.mutation.Where(ps...)
	return sndo
}

// Exec executes the deletion query.
func (sndo *SourceNameDeleteOne) Exec(ctx context.Context) error {
	n, err := sndo.snd.Exec(ctx)
	switch {
	case err != nil:
		return err
	case n == 0:
		return &NotFoundError{sourcename.Label}
	default:
		return nil
	}
}

// ExecX is like Exec, but panics if an error occurs.
func (sndo *SourceNameDeleteOne) ExecX(ctx context.Context) {
	if err := sndo.Exec(ctx); err != nil {
		panic(err)
	}
}
