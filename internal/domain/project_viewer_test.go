package domain

import "testing"

func TestAllProjectsViewer(t *testing.T) {
	v := AllProjectsViewer()
	if !v.AllProjects || v.UserID != nil || v.AgentID != nil || v.ExtensionConnectionID != nil {
		t.Fatalf("AllProjectsViewer() = %+v, want only AllProjects set", v)
	}
	if (ProjectViewer{}).AllProjects {
		t.Fatal("the zero viewer must not see every project")
	}
}
