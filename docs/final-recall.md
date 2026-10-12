# Final recall API

`GET /api/v1/memories/recall-final/v1` performs candidate retrieval, query-aware
reranking, eligibility and noise filtering, optional graph admission, pagination,
and read feedback in one operation. Existing `/memories/search` and
`/memories/recall_graph` endpoints keep their contracts.

Use the same authentication and workspace access as memory search. Parameters
include `q`, `workspace_id`, `project_id`, `scope`, `tags`, `tags_any`, `created_by`,
`source_type`, `since`, `until`, `min_importance`, `relevance_min`, `order_by`,
`apply_recency_decay`, `recency_weight`, `half_life_days`, `exclude_superseded`,
and `include_expired`. `graph` and `include_archived` are optional booleans,
defaulting to false. Scope accepts `agent`, `project`, or `workspace`; omit it
for all scopes. Repeated and comma-separated tags retain search's AND/OR rules.

`limit` is the output page size (default 20, range 1–50). `offset` defaults to
zero and must be in 0–150. Invalid bounds, booleans and non-finite numeric values
return 400 before retrieval or feedback. Minimum importance defaults to 0.3;
explicit zero disables this filter. Client profiles must resolve their defaults
and explicit overrides before calling this endpoint. Response content is complete;
clients apply compact or full presentation after selection.

The retrieval candidate budget is `max(10, offset + limit)`, capped at 200.
Both output5 and output10 therefore rerank at least ten candidates independently.
The existing hybrid arms, structural query/key/project/scope tiers, anchored
0.001 importance bands, and dense noise ceiling of 0.012 are retained. Within
each structural tier, dense memory hits rerank by raw query cosine similarity;
sparse-only hits, documents and pins keep their slots. This prevents dual-arm
RRF rank inflation from burying an answer found by the dense arm alone. Graph
expansion uses at most ten memory seeds, two hops and 200 hydrated neighbours,
without reading them through the feedback-writing memory lookup. A graph row
must pass the query overlap and eligibility gates. Its reserved tail share is
`max(1, limit / 4)` for limits of at least two. Document permissions remain
enforced by document retrieval; their share is capped against the output limit.

The response includes `final_recall_version: 1`, `items`, `total`, `limit`,
`offset`, `candidate_limit`, `graph_boost_count`, `search_mode`, `degraded`,
`dense_rows`, and `sparse_rows`. Retrieval rows carry `provenance: "via:recall"`.
Graph rows carry `graph_boost: true`,
`provenance: "via:graph"`, `hop_distance`, and `composite_score`.

Only unique memory IDs on the final page receive relevance boost and access
touch. Documents, dropped candidates, displaced rows and unreturned graph
seeds do not. Empty pages write no feedback. Feedback remains best effort,
as with search; a database failure can prevent a write without failing recall.

Deploy the API before enabling a client. Clients must require both this route
and version 1, and fail closed if either is unavailable; falling back to
search would apply feedback before final selection. Roll back clients to their
previous version before rolling back the API. No migration is required.
