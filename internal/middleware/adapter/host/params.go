// params.go holds the argument accessors for the Host adapter.
package host

import (
	"encoding/json"
	"fmt"
	"strings"
)

type params map[string]any

func (p params) requireString(name string) (string, error) {
	raw, ok := p[name]
	if !ok {
		return "", fmt.Errorf("host: %s is required", name)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("host: %s must be a string, got %T", name, raw)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("host: %s is required and must not be blank", name)
	}
	return s, nil
}

func (p params) optionalString(name string) (string, error) {
	raw, ok := p[name]
	if !ok || raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("host: %s must be a string, got %T", name, raw)
	}
	return strings.TrimSpace(s), nil
}

func toInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case float32:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("not an integer: %q", v.String())
		}
		return int(n), nil
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err != nil {
			return 0, fmt.Errorf("not an integer: %q", v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", raw)
	}
}
