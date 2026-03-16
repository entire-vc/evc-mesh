// Starter agent example for Entire VC Mesh.
// Demonstrates the full agent lifecycle: authenticate, heartbeat, fetch tasks,
// add a comment, and publish a summary event to the event bus.
//
// Usage:
//
//	export MESH_API_URL=http://localhost:8005
//	export MESH_AGENT_KEY=agk_mywk_xxx
//	go run ./examples/starter-agent
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/entire-vc/evc-mesh/pkg/sdk"
)

func main() {
	apiURL := os.Getenv("MESH_API_URL")
	agentKey := os.Getenv("MESH_AGENT_KEY")

	if apiURL == "" || agentKey == "" {
		log.Fatal("MESH_API_URL and MESH_AGENT_KEY must be set")
	}

	// Authenticate and discover workspace + agent IDs from the API key.
	client, err := sdk.New(apiURL, agentKey)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}

	ctx := context.Background()

	// Identify this agent.
	me, err := client.Me(ctx)
	if err != nil {
		log.Fatalf("me: %v", err)
	}
	fmt.Printf("Agent: %s (%s)  status=%s\n", me.Name, me.AgentType, me.Status)
	fmt.Printf("Workspace: %s\n", me.WorkspaceID)

	// Signal liveness to the server (updates last_heartbeat, sets status=online).
	if heartbeatErr := client.Heartbeat(ctx); heartbeatErr != nil {
		log.Printf("heartbeat warning: %v", heartbeatErr)
	}

	// Fetch tasks assigned to this agent.
	tasks, err := client.GetMyTasks(ctx)
	if err != nil {
		log.Fatalf("get tasks: %v", err)
	}
	fmt.Printf("Assigned tasks: %d\n", len(tasks))

	if len(tasks) == 0 {
		fmt.Println("No tasks assigned — nothing to do.")
		return
	}

	// Work on the first assigned task.
	task := tasks[0]
	fmt.Printf("Working on: [%s] %s  priority=%s\n", task.ID, task.Title, task.Priority)

	// Log start of work as a public comment (visible to humans in the UI).
	_, err = client.AddComment(ctx, task.ID, "Starting work on this task.", false)
	if err != nil {
		log.Printf("add comment: %v", err)
	}

	// ... perform actual work here ...
	result := performWork(task)

	// Log completion with an internal agent-only note.
	internalNote := fmt.Sprintf("Work complete. Result: %s", result)
	_, err = client.AddComment(ctx, task.ID, internalNote, true)
	if err != nil {
		log.Printf("add internal comment: %v", err)
	}

	// Publish a summary event to the project event bus so other agents can
	// pick up the context without polling comments directly.
	_, err = client.PublishEvent(ctx, task.ProjectID, sdk.PublishEventInput{
		EventType: "summary",
		Subject:   fmt.Sprintf("Task %s completed", task.Title),
		Payload: map[string]any{
			"task_id": task.ID,
			"result":  result,
			"status":  "done",
		},
		TaskID: &task.ID,
		Tags:   []string{"completion", "summary"},
	})
	if err != nil {
		log.Printf("publish event: %v", err)
	}

	fmt.Println("Done.")
}

// performWork simulates agent work and returns a result string.
// Replace this with your actual agent logic.
func performWork(task sdk.Task) string {
	return fmt.Sprintf("processed %q", task.Title)
}
