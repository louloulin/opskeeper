package tools

import "github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/tools/toolcore"

// This file is what makes the extraction staged rather than a leap.
//
// These are aliases, not wrappers: `tools.Caller` and `toolcore.Caller`
// are the same type, so a function taking one takes the other and no
// conversion exists anywhere. That matters because the vocabulary now
// lives in toolcore while the hundred-and-some files that use it have not
// moved yet. When a cluster does move, it imports toolcore directly and
// these three lines are the only thing standing between the two states.
//
// They are deleted when the last tool in the package is gone.
type (
	Caller        = toolcore.Caller
	ExecuteResult = toolcore.ExecuteResult
	Tool          = toolcore.Tool
)
