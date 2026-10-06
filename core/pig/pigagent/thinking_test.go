package pigagent

import "testing"

// TestStripInlineThinking pins the three cases that decide whether an operator
// sees the model's private reasoning: a reasoning run followed by an answer, a
// message that is nothing but reasoning, and a partial stream that has opened
// a tag but not closed it.
func TestStripInlineThinking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "reasoning before the answer is removed",
			in:   "<think>The user wants a greeting.</think>\n\nHello.",
			want: "Hello.",
		},
		{
			name: "reasoning between answers is removed",
			in:   "first.<think>hm</think>second.",
			want: "first.second.",
		},
		{
			name: "an answer with no reasoning is untouched",
			in:   "Hello.",
			want: "Hello.",
		},
		{
			name: "an unterminated opening tag is left alone",
			in:   "<think>still thinking",
			want: "<think>still thinking",
		},
		{
			name: "reasoning-only keeps a body so the turn still records",
			in:   "<think>all reasoning, no prose</think>",
			want: "<think>all reasoning, no prose</think>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stripInlineThinking(tc.in); got != tc.want {
				t.Errorf("stripInlineThinking(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
