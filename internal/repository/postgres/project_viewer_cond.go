package postgres

import (
	"fmt"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// projectViewerCond is the SQL form of domain.ProjectViewer over a projects
// row aliased `p`: the condition a query ANDs in so that only projects the
// viewer may open contribute. Placeholders continue after args; the zero
// viewer yields FALSE (sees nothing).
func projectViewerCond(v domain.ProjectViewer, args []interface{}) (cond string, outArgs []interface{}) {
	if v.ExtensionConnectionID != nil {
		args = append(args, *v.ExtensionConnectionID)
		return fmt.Sprintf("EXISTS (SELECT 1 FROM integration_configs ic WHERE ic.id = $%d AND%s)",
			len(args), extensionProjectAllowed), args
	}
	if v.AllProjects {
		return "TRUE", args
	}
	var ids []string
	if v.AgentID != nil {
		args = append(args, *v.AgentID)
		ids = append(ids, fmt.Sprintf("pm.agent_id = $%d", len(args)))
	}
	if v.UserID != nil {
		args = append(args, *v.UserID)
		ids = append(ids, fmt.Sprintf("pm.user_id = $%d", len(args)))
	}
	if len(ids) == 0 {
		return "FALSE", args
	}
	return "EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND (" +
		strings.Join(ids, " OR ") + "))", args
}
