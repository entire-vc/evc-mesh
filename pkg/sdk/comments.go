package sdk

import (
	"context"
	"fmt"
	"net/url"
)

// Comment represents a threaded comment on a task.
type Comment struct {
	ID              string  `json:"id"`
	TaskID          string  `json:"task_id"`
	ParentCommentID *string `json:"parent_comment_id,omitempty"`
	AuthorID        string  `json:"author_id"`
	AuthorType      string  `json:"author_type"` // user|agent|system
	Body            string  `json:"body"`
	IsInternal      bool    `json:"is_internal"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// commentPage mirrors pagination.Page[Comment] from the server.
type commentPage struct {
	Items      []Comment `json:"items"`
	TotalCount int       `json:"total_count"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
	TotalPages int       `json:"total_pages"`
	HasMore    bool      `json:"has_more"`
}

// addCommentBody is the request body for POST /tasks/:id/comments.
type addCommentBody struct {
	Body            string  `json:"body"`
	ParentCommentID *string `json:"parent_comment_id,omitempty"`
	IsInternal      bool    `json:"is_internal"`
}

// AddComment adds a comment to a task.
// Set isInternal=true for agent-only internal notes.
func (c *Client) AddComment(ctx context.Context, taskID, body string, isInternal bool) (*Comment, error) {
	req := addCommentBody{Body: body, IsInternal: isInternal}
	var comment Comment
	if err := c.post(ctx, "/api/v1/tasks/"+taskID+"/comments", req, &comment); err != nil {
		return nil, fmt.Errorf("AddComment: %w", err)
	}
	return &comment, nil
}

// ReplyToComment adds a threaded reply to an existing comment.
func (c *Client) ReplyToComment(ctx context.Context, taskID, parentCommentID, body string) (*Comment, error) {
	req := addCommentBody{Body: body, ParentCommentID: &parentCommentID}
	var comment Comment
	if err := c.post(ctx, "/api/v1/tasks/"+taskID+"/comments", req, &comment); err != nil {
		return nil, fmt.Errorf("ReplyToComment: %w", err)
	}
	return &comment, nil
}

// ListComments returns comments for a task.
// Set includeInternal=true to include agent-only internal comments.
func (c *Client) ListComments(ctx context.Context, taskID string, includeInternal bool) ([]Comment, error) {
	q := url.Values{}
	if includeInternal {
		q.Set("include_internal", "true")
	}
	path := "/api/v1/tasks/" + taskID + "/comments"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var page commentPage
	if err := c.get(ctx, path, &page); err != nil {
		return nil, fmt.Errorf("ListComments: %w", err)
	}
	return page.Items, nil
}

// DeleteComment deletes a comment by ID.
func (c *Client) DeleteComment(ctx context.Context, commentID string) error {
	if err := c.delete(ctx, "/api/v1/comments/"+commentID); err != nil {
		return fmt.Errorf("DeleteComment: %w", err)
	}
	return nil
}
