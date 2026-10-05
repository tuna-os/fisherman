package validation

import (
	"testing"

	"github.com/tuna-os/fisherman/internal/recipe"
)

// MockValidator is a test double for Validator.
type MockValidator struct {
	name string
	err  error
}

func (m *MockValidator) Name() string {
	return m.name
}

func (m *MockValidator) Validate() error {
	return m.err
}

func TestChainRunAllPass(t *testing.T) {
	chain := NewChain().
		Add(&MockValidator{name: "pass1", err: nil}).
		Add(&MockValidator{name: "pass2", err: nil})

	if err := chain.Run(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestChainRunStopsAtFirstFailure(t *testing.T) {
	chain := NewChain().
		Add(&MockValidator{name: "pass", err: nil}).
		Add(&MockValidator{name: "fail", err: ErrMockFailure}).
		Add(&MockValidator{name: "never_run", err: ErrMockFailure})

	err := chain.Run()
	if err == nil {
		t.Error("expected error, got nil")
	}
	if err.Error() != "fail: mock failure" {
		t.Errorf("expected 'fail: mock failure', got %q", err.Error())
	}
}

func TestChainRunEmpty(t *testing.T) {
	chain := NewChain()
	if err := chain.Run(); err != nil {
		t.Errorf("empty chain should pass, got %v", err)
	}
}

var ErrMockFailure = errorString("mock failure")

type errorString string

func (e errorString) Error() string {
	return string(e)
}

func TestRecipeValidator(t *testing.T) {
	tests := []struct {
		name    string
		recipe  *recipe.Recipe
		wantErr bool
	}{
		{
			name:    "nil recipe",
			recipe:  nil,
			wantErr: true,
		},
		// Note: full recipe validation is tested in recipe package
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewRecipeValidator(tt.recipe)
			err := v.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToolValidator(t *testing.T) {
	tests := []struct {
		name      string
		tools     []string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "empty tool list",
			tools:   []string{},
			wantErr: false,
		},
		{
			name:      "nonexistent tool",
			tools:     []string{"/nonexistent/tool/xyz"},
			wantErr:   true,
			errSubstr: "missing",
		},
		{
			name:    "sh should exist",
			tools:   []string{"sh"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewToolValidator(tt.tools)
			err := v.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && tt.errSubstr != "" && (err == nil || !contains(err.Error(), tt.errSubstr)) {
				t.Errorf("expected error containing %q, got %v", tt.errSubstr, err)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
