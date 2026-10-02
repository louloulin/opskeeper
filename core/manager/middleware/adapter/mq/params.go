// params.go holds the argument accessors for the MQ adapter.
//
// Args arrive as map[string]any, and a JSON number may have decoded as
// float64, json.Number or int depending on the path it travelled, so every
// accessor funnels through toInt. A remediation must not fail with "expected
// number, got float64" at the moment it is most needed.
package mq

import (
	"encoding/json"
	"fmt"
	"strings"
)

type params map[string]any

func (p params) requireString(name string) (string, error) {
	raw, ok := p[name]
	if !ok {
		return "", fmt.Errorf("mq: %s is required", name)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("mq: %s must be a string, got %T", name, raw)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("mq: %s is required and must not be blank", name)
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
		return "", fmt.Errorf("mq: %s must be a string, got %T", name, raw)
	}
	return strings.TrimSpace(s), nil
}

func (p params) optionalBool(name string, def bool) (bool, error) {
	raw, ok := p[name]
	if !ok || raw == nil {
		return def, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return def, fmt.Errorf("mq: %s must be a boolean, got %T", name, raw)
	}
	return b, nil
}

func intArg(p map[string]interface{}, name string, def int, max int) (int, error) {
	raw, ok := p[name]
	if !ok {
		return def, nil
	}
	v, err := toInt(raw)
	if err != nil {
		return 0, fmt.Errorf("mq: %s: %w", name, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("mq: %s must be positive, got %d", name, v)
	}
	if max > 0 && v > max {
		// A bounded batch is not a limitation to work around: "replay
		// everything" against an unbounded queue is a decision that belongs
		// to a human with a number in front of them, not to a default.
		return 0, fmt.Errorf("mq: %s must be at most %d, got %d", name, max, v)
	}
	return v, nil
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

// asInt reads a number out of a decoded JSON document.
func asInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, err := toInt(m[key])
	if err != nil {
		return 0
	}
	return v
}

func asFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func asString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
