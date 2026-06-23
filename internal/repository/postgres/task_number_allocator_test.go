package postgres

import (
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestIsTaskNumberConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "exact constraint match",
			err:  &pq.Error{Code: "23505", Constraint: "uq_tasks_project_number"},
			want: true,
		},
		{
			name: "different constraint name",
			err:  &pq.Error{Code: "23505", Constraint: "uq_something_else"},
			want: false,
		},
		{
			name: "not a unique violation",
			err:  &pq.Error{Code: "23503", Constraint: "uq_tasks_project_number"},
			want: false,
		},
		{
			name: "plain error",
			err:  fmt.Errorf("some error"),
			want: false,
		},
		{
			name: "nil",
			err:  nil,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTaskNumberConflict(tc.err); got != tc.want {
				t.Errorf("isTaskNumberConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
