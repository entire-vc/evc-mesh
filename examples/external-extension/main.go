// Reference external extension for Entire VC Mesh.
//
// It binds to a workspace through the install handshake, receives its
// extension token on the token path, verifies signed events and answers a new
// task with a comment, and serves two iframe screens (sidebar and task panel).
//
// Usage:
//
//	export MESH_URL=https://mesh.example.com   # Mesh origin: API calls and the iframe token issuer
//	export INSTALL_CODE=...                      # optional binding code, see README
//	go run ./examples/external-extension
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/entire-vc/evc-mesh/examples/external-extension/refext"
)

const extensionID = "reference-ext"

func main() {
	meshURL := strings.TrimRight(os.Getenv("MESH_URL"), "/")
	if meshURL == "" {
		log.Fatal("MESH_URL is required")
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8090"
	}

	client := &refext.Client{BaseURL: meshURL}
	srv := &refext.Server{
		ExtensionID: extensionID,
		UI:          &refext.ContextVerifier{MeshOrigin: meshURL, ExtensionID: extensionID},
	}
	// The binding code, spent by the first install that uses it. Without one a
	// reachable service could be bound by anyone who gets there before Mesh.
	code := os.Getenv("INSTALL_CODE")
	if code == "" {
		log.Fatal("INSTALL_CODE is required: issue a code and store it in Mesh as the workspace secret EXT_REFERENCE_EXT_INSTALL_CODE")
	}
	srv.IssueInstallCode(code)
	// Which task gets a comment is decided from the signed body only: the event
	// name header is not signed in v1, so it is logged, never trusted to pick
	// a side effect. One comment per task, whatever replays arrive.
	var mu sync.Mutex
	commented := map[string]bool{}
	srv.OnEvent = func(ctx context.Context, ev refext.Event) error {
		log.Printf("event %q (delivery %q) in workspace %q", ev.Name, ev.DeliveryID, ev.WorkspaceID)
		var taskID string
		if err := json.Unmarshal(ev.Data["task_id"], &taskID); err != nil || taskID == "" {
			return nil // nothing to answer; a retry would not change that
		}
		key := ev.WorkspaceID + "/" + taskID
		mu.Lock()
		done := commented[key]
		commented[key] = true
		mu.Unlock()
		if done {
			return nil
		}
		if err := client.AddComment(ctx, srv.Token(ev.WorkspaceID), taskID, "Seen by the reference extension."); err != nil {
			mu.Lock()
			delete(commented, key) // the retry should try again
			mu.Unlock()
			return err
		}
		return nil
	}

	s := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("reference extension listening on %s (Mesh %s)", addr, meshURL)
	log.Fatal(s.ListenAndServe())
}
