package metrics

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TaskDistributionCollector is a custom Prometheus Collector that emits
// mesh_tasks gauges from a live DB query on each scrape.
type TaskDistributionCollector struct {
	db   *sql.DB
	desc *prometheus.Desc
}

// NewTaskDistributionCollector creates a collector backed by the given *sql.DB.
// Register it once with prometheus.MustRegister at startup.
func NewTaskDistributionCollector(db *sql.DB) *TaskDistributionCollector {
	return &TaskDistributionCollector{
		db: db,
		desc: prometheus.NewDesc(
			"mesh_tasks",
			"Current task count by project, status, and assignee_type.",
			[]string{"project", "status", "assignee_type"},
			nil,
		),
	}
}

func (c *TaskDistributionCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *TaskDistributionCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const q = `
		SELECT p.slug, ts.name, t.assignee_type, COUNT(*) AS cnt
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN projects p ON p.id = t.project_id
		WHERE t.deleted_at IS NULL
		  AND ts.category::text NOT IN ('done', 'cancelled')
		GROUP BY p.slug, ts.name, t.assignee_type
		LIMIT 1000
	`
	rows, err := c.db.QueryContext(ctx, q)
	if err != nil {
		log.Printf("[task-distribution-collector] query failed: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var project, status, assigneeType string
		var cnt float64
		if err := rows.Scan(&project, &status, &assigneeType, &cnt); err != nil {
			log.Printf("[task-distribution-collector] scan failed: %v", err)
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, cnt, project, status, assigneeType)
	}
}
