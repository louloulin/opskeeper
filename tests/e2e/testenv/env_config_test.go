//go:build e2e

package testenv

import (
	"testing"
	"time"
)

func TestMySQLWaitTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		got, err := mysqlWaitTimeout()
		if err != nil {
			t.Fatalf("mysqlWaitTimeout() error = %v", err)
		}
		if got != 5*time.Minute {
			t.Fatalf("mysqlWaitTimeout() = %v, want 5m", got)
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv("OPSKEEPER_E2E_MYSQL_WAIT", "90s")
		got, err := mysqlWaitTimeout()
		if err != nil {
			t.Fatalf("mysqlWaitTimeout() error = %v", err)
		}
		if got != 90*time.Second {
			t.Fatalf("mysqlWaitTimeout() = %v, want 90s", got)
		}
	})

	for name, value := range map[string]string{
		"invalid": "soon",
		"zero":    "0s",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPSKEEPER_E2E_MYSQL_WAIT", value)
			if _, err := mysqlWaitTimeout(); err == nil {
				t.Fatalf("mysqlWaitTimeout() error = nil, want error")
			}
		})
	}
}
