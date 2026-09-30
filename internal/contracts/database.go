package contracts

import "fmt"

// Migration is one forward-only schema change owned by a feature.
type Migration struct {
	Scope       string
	Version     int
	Description string
	Statements  []string
}

// Validate checks the migration identity rules shared with the Python runner.
func (m Migration) Validate() error {
	if !validScope(m.Scope) {
		return fmt.Errorf("migration scope must contain letters, numbers or underscores")
	}
	if m.Version <= 0 {
		return fmt.Errorf("migration version must be positive")
	}
	if len(m.Statements) == 0 {
		return fmt.Errorf("migration statements must not be empty")
	}
	for _, statement := range m.Statements {
		blank := true
		for _, r := range statement {
			if r != ' ' && r != '\n' && r != '\t' && r != '\r' {
				blank = false
				break
			}
		}
		if blank {
			return fmt.Errorf("migration statements must not be empty")
		}
	}
	return nil
}

func validScope(scope string) bool {
	if scope == "" {
		return false
	}
	stripped := ""
	for _, r := range scope {
		if r == '_' {
			continue
		}
		stripped += string(r)
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return stripped != ""
}

// MigrationDriftError means an applied migration's SQL changed.
type MigrationDriftError struct {
	Scope   string
	Version int
}

func (e *MigrationDriftError) Error() string {
	return fmt.Sprintf("applied migration %s:%d changed", e.Scope, e.Version)
}

// DatabaseError is a driver-neutral database failure.
type DatabaseError struct{ Err error }

func (e *DatabaseError) Error() string {
	if e.Err == nil {
		return "database error"
	}
	return e.Err.Error()
}

func (e *DatabaseError) Unwrap() error { return e.Err }

// IntegrityViolation is a unique or foreign-key failure.
type IntegrityViolation struct{ Err error }

func (e *IntegrityViolation) Error() string {
	if e.Err == nil {
		return "integrity violation"
	}
	return e.Err.Error()
}

func (e *IntegrityViolation) Unwrap() error { return e.Err }
