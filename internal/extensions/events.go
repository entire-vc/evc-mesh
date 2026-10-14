package extensions

// EventsVersion is the version of the public event list below. It follows the
// extension API rules in docs/extensions/api-version.md: removing an event or
// changing its payload incompatibly raises ExtensionsAPIVersion; adding an
// event does not.
const EventsVersion = 1

// Public event names an extension can subscribe to (spec 03 §8). The names are
// part of the contract: never rename or reuse one.
const (
	EventTaskCreated     = "task.created"
	EventTaskUpdated     = "task.updated"
	EventTaskMoved       = "task.moved"
	EventTaskAssigned    = "task.assigned"
	EventTaskCompleted   = "task.completed"
	EventTaskGateDecided = "task.gate.decided"

	EventCommentCreated   = "comment.created"
	EventArtifactUploaded = "artifact.uploaded"
	EventDocUpdated       = "doc.updated"

	EventProjectCreated = "project.created"
	EventProjectUpdated = "project.updated"

	EventWorkspaceMemberAdded   = "workspace.member.added"
	EventWorkspaceMemberRemoved = "workspace.member.removed"

	EventAgentKeyIssued  = "agent.key.issued"
	EventAgentKeyRevoked = "agent.key.revoked"

	EventExtensionInstalled = "extension.installed"
	EventExtensionConnected = "extension.connected"
	EventExtensionDisabled  = "extension.disabled"
	EventExtensionUpdated   = "extension.updated"
)

// publicEvents keeps the documented order.
var publicEvents = []string{
	EventTaskCreated, EventTaskUpdated, EventTaskMoved, EventTaskAssigned, EventTaskCompleted,
	EventTaskGateDecided,
	EventCommentCreated, EventArtifactUploaded, EventDocUpdated,
	EventProjectCreated, EventProjectUpdated,
	EventWorkspaceMemberAdded, EventWorkspaceMemberRemoved,
	EventAgentKeyIssued, EventAgentKeyRevoked,
	EventExtensionInstalled, EventExtensionConnected, EventExtensionDisabled, EventExtensionUpdated,
}

// PublicEvents returns a copy of the public event list.
func PublicEvents() []string {
	return append([]string(nil), publicEvents...)
}

// IsPublicEvent reports whether name is in the public event list.
func IsPublicEvent(name string) bool {
	for _, e := range publicEvents {
		if e == name {
			return true
		}
	}
	return false
}
