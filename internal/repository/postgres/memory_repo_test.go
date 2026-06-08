package postgres

import (
	"testing"
)

func TestTokenizeForORQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "natural language with filler words",
			input: "mesh deploy unblocked by fixing migrate-gate DSN",
			// "by" dropped (<3), "fixing"+"migrate"+"gate"+"dsn"+"mesh"+"deploy"+"unblocked" kept
			want: "mesh | deploy | unblocked | fixing | migrate | gate | dsn",
		},
		{
			name:  "short stopwords filtered",
			input: "fix for the bug in it",
			// "for","the","in","it" all <3 or exactly 3 — "the" is 3 chars, "for" is 3 chars, kept; "in" and "it" are 2 chars, dropped
			want: "fix | for | the | bug",
		},
		{
			name:  "deduplication",
			input: "mesh mesh api mesh endpoint",
			want:  "mesh | api | endpoint",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "only short tokens",
			input: "by an or a",
			want:  "",
		},
		{
			name:  "mixed case normalized",
			input: "Dispatcher Fix Pavel HUMAN-VERIFY",
			want:  "dispatcher | fix | pavel | human | verify",
		},
		{
			name:  "typical episodic golden query",
			input: "argus confidence based person dedup merge shipped",
			want:  "argus | confidence | based | person | dedup | merge | shipped",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeForORQuery(tc.input)
			if got != tc.want {
				t.Errorf("tokenizeForORQuery(%q)\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}
}
