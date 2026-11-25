package domain

import "testing"

func TestReadSourceOf(t *testing.T) {
	for _, tc := range []struct {
		sourceType MemorySourceType
		scope      MemoryScope
		want       string
	}{
		{SourceDoc, ScopeProject, ReadSourceDoc},
		{SourceDoc, ScopeWorkspace, ReadSourceDoc},
		{SourceAgent, ScopeProject, ReadSourceProjectKnowledge},
		{SourceAgent, ScopeWorkspace, ReadSourceMemory},
		{"", "", ReadSourceMemory},
	} {
		if got := ReadSourceOf(tc.sourceType, tc.scope); got != tc.want {
			t.Errorf("ReadSourceOf(%q, %q) = %q, want %q", tc.sourceType, tc.scope, got, tc.want)
		}
	}
}

func TestValidCallerKind(t *testing.T) {
	for _, v := range []string{CallerKindAgentTool, CallerKindRunner, CallerKindScript, CallerKindUI, CallerKindUnknown} {
		if !ValidCallerKind(v) {
			t.Errorf("ValidCallerKind(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "Agent_Tool", "agent", "browser"} {
		if ValidCallerKind(v) {
			t.Errorf("ValidCallerKind(%q) = true, want false", v)
		}
	}
}
