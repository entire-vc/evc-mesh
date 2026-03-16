// Orchestrator pattern for Entire VC Mesh.
// An orchestrator agent breaks work into sub-tasks, spawns child agents for each,
// polls for completion, and publishes a consolidated summary to the event bus.
//
// Run alongside main.go:
//
//	export MESH_API_URL=http://localhost:8005
//	export MESH_ORCHESTRATOR_KEY=agk_mywk_orchestrator_xxx
//	go run ./examples/starter-agent -mode=orchestrate
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/pkg/sdk"
)

// SubAgentConfig describes a child agent to spawn for a specific work slice.
type SubAgentConfig struct {
	Name         string
	AgentType    string
	Capabilities map[string]any
}

// OrchestratorResult collects outputs from all sub-agents.
type OrchestratorResult struct {
	TaskID   string
	Subtasks []SubtaskResult
}

// SubtaskResult holds the outcome of a single sub-agent's work.
type SubtaskResult struct {
	SubtaskID string
	Title     string
	AgentName string
	Done      bool
}

// Orchestrate is the main entry point for the orchestrator pattern.
// It:
//  1. Fetches the first assigned task.
//  2. Discovers or registers child agents.
//  3. Creates one subtask per child agent.
//  4. Polls until all subtasks reach the "done" category.
//  5. Publishes a consolidated summary event.
func Orchestrate(ctx context.Context, client *sdk.Client) error {
	// Get tasks assigned to this orchestrator.
	tasks, err := client.GetMyTasks(ctx)
	if err != nil {
		return fmt.Errorf("GetMyTasks: %w", err)
	}
	if len(tasks) == 0 {
		fmt.Println("Orchestrator: no tasks assigned.")
		return nil
	}

	parentTask := tasks[0]
	fmt.Printf("Orchestrating: [%s] %s\n", parentTask.ID, parentTask.Title)

	// Define child agent configurations.
	subAgentConfigs := []SubAgentConfig{
		{
			Name:      "research-agent",
			AgentType: "custom",
			Capabilities: map[string]any{
				"roles": []string{"research", "web-search"},
			},
		},
		{
			Name:      "writer-agent",
			AgentType: "custom",
			Capabilities: map[string]any{
				"roles": []string{"writing", "summarization"},
			},
		},
	}

	// Spawn sub-agents and create one subtask per agent.
	results := make([]SubtaskResult, 0, len(subAgentConfigs))

	for _, cfg := range subAgentConfigs {
		subAgent, regErr := client.RegisterSubAgent(ctx, sdk.RegisterSubAgentInput{
			Name:         cfg.Name,
			AgentType:    cfg.AgentType,
			Capabilities: cfg.Capabilities,
		})
		if regErr != nil {
			log.Printf("register sub-agent %q: %v — skipping", cfg.Name, regErr)
			continue
		}
		fmt.Printf("  Spawned sub-agent: %s (id=%s)\n", subAgent.Name, subAgent.ID)

		// Create a subtask under the parent task and assign it to the child agent.
		subtask, subErr := client.CreateSubtask(ctx, parentTask.ID, sdk.CreateSubtaskInput{
			Title:    fmt.Sprintf("[%s] handle portion of: %s", cfg.Name, parentTask.Title),
			Priority: parentTask.Priority,
		})
		if subErr != nil {
			log.Printf("create subtask for %q: %v — skipping", cfg.Name, subErr)
			continue
		}

		// Assign subtask to the new sub-agent.
		_, assignErr := client.AssignTask(ctx, subtask.ID, subAgent.ID, "agent")
		if assignErr != nil {
			log.Printf("assign subtask %s to %s: %v", subtask.ID, subAgent.ID, assignErr)
		}

		results = append(results, SubtaskResult{
			SubtaskID: subtask.ID,
			Title:     subtask.Title,
			AgentName: subAgent.Name,
		})
	}

	// Announce dispatch to the project event bus.
	_, _ = client.PublishEvent(ctx, parentTask.ProjectID, sdk.PublishEventInput{
		EventType: "context_update",
		Subject:   fmt.Sprintf("Dispatched %d sub-agents for task %s", len(results), parentTask.Title),
		Payload: map[string]any{
			"parent_task_id": parentTask.ID,
			"sub_agents":     len(results),
		},
		TaskID: &parentTask.ID,
		Tags:   []string{"orchestration", "dispatch"},
	})

	// Poll until all subtasks complete (done/cancelled category) or timeout.
	if pollErr := pollSubtasks(ctx, client, parentTask.ProjectID, results); pollErr != nil {
		return fmt.Errorf("poll subtasks: %w", pollErr)
	}

	// Publish consolidated summary.
	doneCount := 0
	for _, r := range results {
		if r.Done {
			doneCount++
		}
	}

	_, err = client.PublishEvent(ctx, parentTask.ProjectID, sdk.PublishEventInput{
		EventType: "summary",
		Subject:   fmt.Sprintf("Orchestration complete: %d/%d subtasks done", doneCount, len(results)),
		Payload: map[string]any{
			"parent_task_id": parentTask.ID,
			"total":          len(results),
			"done":           doneCount,
		},
		TaskID: &parentTask.ID,
		Tags:   []string{"orchestration", "summary"},
	})
	if err != nil {
		log.Printf("publish summary event: %v", err)
	}

	fmt.Printf("Orchestration complete: %d/%d subtasks done\n", doneCount, len(results))
	return nil
}

// pollSubtasks checks subtask statuses repeatedly until all are in a terminal
// category (done or cancelled) or the deadline is reached.
func pollSubtasks(ctx context.Context, client *sdk.Client, projectID string, results []SubtaskResult) error {
	deadline := time.Now().Add(30 * time.Minute)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("polling deadline exceeded")
			}

			// Fetch current statuses for the project.
			statuses, err := client.ListStatuses(ctx, projectID)
			if err != nil {
				log.Printf("list statuses: %v", err)
				continue
			}

			// Build a map of status_id -> category.
			catByID := make(map[string]string, len(statuses))
			for _, s := range statuses {
				catByID[s.ID] = s.Category
			}

			allDone := true
			for i, r := range results {
				if r.Done {
					continue
				}
				subtask, err := client.GetTask(ctx, r.SubtaskID)
				if err != nil {
					log.Printf("get subtask %s: %v", r.SubtaskID, err)
					allDone = false
					continue
				}

				cat := catByID[subtask.StatusID]
				if cat == "done" || cat == "cancelled" {
					results[i].Done = true
					fmt.Printf("  Subtask done: %s (agent=%s)\n", r.Title, r.AgentName)
				} else {
					allDone = false
				}
			}

			if allDone {
				return nil
			}
			fmt.Printf("  Waiting for %d subtask(s)...\n", countPending(results))
		}
	}
}

func countPending(results []SubtaskResult) int {
	n := 0
	for _, r := range results {
		if !r.Done {
			n++
		}
	}
	return n
}
