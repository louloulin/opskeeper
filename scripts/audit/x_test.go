package main

import "testing"

func TestX(t *testing.T) {
	s := "package pluginmanifest\n\nvar DiagnosisGaps = map[string]GapReason{\n\t\"x\": {Reason: \"r\"},\n}\n"
	t.Log("count", countGaps(s))
}
