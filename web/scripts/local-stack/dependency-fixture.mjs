import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

// Disposable local-stack fixture. Never point this generator at a live service.
const apiURL = process.env.LOCAL_STACK_API_URL || "http://localhost:8147";
const webURL = process.env.LOCAL_STACK_WEB_URL || "http://localhost:3147";
for (const url of [apiURL, webURL]) {
  if (!["localhost", "127.0.0.1"].includes(new URL(url).hostname)) throw new Error("Local stack required");
}
const container = process.env.LOCAL_STACK_PG_CONTAINER || "mesh-local-stack-pg-14580276";
if (!/^mesh-local-stack-pg-[a-z0-9-]+$/.test(container)) throw new Error("Disposable database required");
const output = process.env.DEPENDENCY_FIXTURE || "evidence/14580276/fixture.json";
async function request(path, data, token) {
  const response = await fetch(apiURL + "/api/v1" + path, {
    method: data ? "POST" : "GET",
    headers: { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    ...(data ? { body: JSON.stringify(data) } : {}),
  });
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
  return response.json();
}
const login = await request("/auth/login", {
  email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
});
const token = login.tokens.access_token;
const workspaces = await request("/workspaces", null, token);
const workspace = workspaces.find(w => w.slug === "dependencies-14580276") ||
  await request("/workspaces", { name: "Dependencies 14580276", slug: "dependencies-14580276" }, token);
const result = await request(`/workspaces/${workspace.id}/projects`, null, token);
const project = (result.items || result).find(p => p.slug === "dense-dependencies") ||
  await request(`/workspaces/${workspace.id}/projects`, { name: "Dense Dependencies", slug: "dense-dependencies" }, token);
const statuses = await request(`/projects/${project.id}/statuses`, null, token);
const todo = statuses.find(s => s.category === "todo"), done = statuses.find(s => s.category === "done");
for (const value of [project.id, login.user.id, todo?.id, done?.id]) {
  if (!/^[0-9a-f-]{36}$/.test(value || "")) throw new Error("Invalid fixture identity/status");
}
const id = n => "14580276-0000-4000-8000-" + String(n).padStart(12, "0");
const edges = new Set();
for (let i = 2; i <= 1000; i++) for (let d = 1; d <= 3 && i - d >= 1; d++) edges.add(`${i - d},${i}`);
for (let i = 1; i <= 20; i++) edges.add(`${i},500`);
for (let i = 501; i <= 520; i++) edges.add(`500,${i}`);
for (const edge of [...edges].reverse()) {
  if (edges.size <= 3000) break;
  if (!edge.split(",").includes("500")) edges.delete(edge);
}
const sql = `BEGIN;
-- Historical topology is retained by the graph reader, but new completion
-- graph writes reject it. Seed only this disposable DB under transaction locks;
-- restore both write guards before committing so later browser/API writes retain them.
ALTER TABLE tasks DISABLE TRIGGER completion_graph_tasks;
ALTER TABLE task_dependencies DISABLE TRIGGER completion_graph_dependencies;
INSERT INTO tasks(id,project_id,status_id,title,priority,task_number,created_by,parent_task_id)
SELECT ('14580276-0000-4000-8000-'||lpad(i::text,12,'0'))::uuid,'${project.id}',
CASE WHEN i%10=1 THEN '${done.id}'::uuid ELSE '${todo.id}'::uuid END,
CASE WHEN i=500 THEN 'Verify access boundaries and release dependencies'
ELSE 'Dependency task '||i||': browser contracts, permissions and delivery' END,
'high',i,'${login.user.id}',CASE WHEN i>20 AND i<400 THEN '${id(10)}'::uuid ELSE NULL END
FROM generate_series(1,1000) i ON CONFLICT(id) DO NOTHING;
INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES
${[...edges].map((edge, i) => {
  const [a, b] = edge.split(",").map(Number);
  return `('${id(10000 + i)}','${id(b)}','${id(a)}','blocks')`;
}).join(",")} ON CONFLICT(id) DO NOTHING;
ALTER TABLE tasks ENABLE TRIGGER completion_graph_tasks;
ALTER TABLE task_dependencies ENABLE TRIGGER completion_graph_dependencies;
COMMIT;
SELECT 'tasks='||count(*) FROM tasks WHERE project_id='${project.id}';
SELECT 'edges='||count(*) FROM task_dependencies WHERE task_id IN (SELECT id FROM tasks WHERE project_id='${project.id}');`;
const counts = execFileSync("docker", ["exec", "-i", container, "psql", "-tA", "-v", "ON_ERROR_STOP=1", "-U", "mesh", "-d", "mesh"], { input: sql, encoding: "utf8" });
if (!counts.includes("tasks=1000") || !counts.includes("edges=3000")) throw new Error("Unexpected fixture counts");
mkdirSync(dirname(output), { recursive: true });
const focus = id(500);
writeFileSync(output, JSON.stringify({ workspace, project, focus,
  url: `${webURL}/w/${workspace.slug}/p/${project.slug}/timeline?focus=${focus}` }, null, 2));
console.log(`FIXTURE tasks=1000 edges=3000 output=${output}`);
