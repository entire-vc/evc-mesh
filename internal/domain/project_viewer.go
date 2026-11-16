package domain

import "github.com/google/uuid"

// ProjectViewer is who a workspace-wide read is served to, as far as project
// data is concerned: figures and names that come out of projects may only
// reach someone who could open those projects. The rule is the project access
// middleware's (UserMayAccessProject): a human workspace owner/admin sees every
// project (AllProjects), anyone else only the projects they are a member of;
// an agent only its explicit memberships; an extension the projects its
// connection may reach (all of the workspace, or the bound ones).
//
// The zero value sees NOTHING, so a read path that does not say who is asking
// gets no project data rather than all of it.
type ProjectViewer struct {
	UserID      *uuid.UUID
	AgentID     *uuid.UUID
	AllProjects bool

	// ExtensionConnectionID narrows an extension actor to the projects its
	// project-bound connection may reach.
	ExtensionConnectionID *uuid.UUID
}

// AllProjectsViewer sees every project of the workspace.
func AllProjectsViewer() ProjectViewer { return ProjectViewer{AllProjects: true} }
