package security

import "fmt"

type Operation string

const (
	OperationRead  Operation = "read"
	OperationWrite Operation = "write"
	OperationAdmin Operation = "admin"
)

type PermissionChecker struct {
	permissions map[string]string
}

func NewPermissionChecker(permissions map[string]string) *PermissionChecker {
	copied := make(map[string]string, len(permissions))
	for k, v := range permissions {
		copied[k] = v
	}
	return &PermissionChecker{permissions: copied}
}

func (p *PermissionChecker) Allowed(connector string, op Operation) error {
	level := p.permissions[connector]
	if level == "" {
		level = "read"
	}
	if allows(level, op) {
		return nil
	}
	return fmt.Errorf("permission denied: connector=%s operation=%s configured=%s", connector, op, level)
}

func allows(level string, op Operation) bool {
	switch level {
	case "admin":
		return true
	case "write":
		return op == OperationRead || op == OperationWrite
	default:
		return op == OperationRead
	}
}
