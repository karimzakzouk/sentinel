package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", ErrAlreadyExists, true},
		{"wrapped", fmt.Errorf("create namespace: %w", ErrAlreadyExists), true},
		{"substring lower", errors.New("namespaces \"foo\" already exists"), true},
		{"substring mixed", errors.New("Resource Already Exists"), true},
		{"unrelated", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAlreadyExists(tt.err); got != tt.want {
				t.Errorf("IsAlreadyExists(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
