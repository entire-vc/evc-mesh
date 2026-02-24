// k6 Load Test: CRUD Scenario for evc-mesh API
//
// Simulates realistic user behavior: register, create project, CRUD tasks.
//
// Usage:
//   k6 run tests/load/crud-scenario.js
//   k6 run tests/load/crud-scenario.js --env BASE_URL=http://staging:8005
//
// Environment variables:
//   BASE_URL  - API base URL (default: http://localhost:8005)

import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// --- Configuration ---

export const options = {
    stages: [
        { duration: '30s', target: 20 },  // Ramp up to 20 VUs
        { duration: '1m',  target: 50 },  // Sustained load at 50 VUs
        { duration: '30s', target: 100 }, // Peak load at 100 VUs
        { duration: '30s', target: 0 },   // Ramp down
    ],
    thresholds: {
        http_req_duration: ['p(95)<500'],     // 95th percentile under 500ms
        http_req_failed: ['rate<0.05'],       // Less than 5% failure rate
        'task_create_duration': ['p(95)<300'], // Task creation under 300ms
        'task_list_duration': ['p(95)<200'],   // Task listing under 200ms
    },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8005';

// --- Custom Metrics ---

const taskCreateDuration = new Trend('task_create_duration', true);
const taskListDuration = new Trend('task_list_duration', true);
const taskUpdateDuration = new Trend('task_update_duration', true);
const errorRate = new Rate('errors');
const taskCounter = new Counter('tasks_created');

// --- Helpers ---

function headers(token) {
    return {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`,
    };
}

function randomString(length) {
    const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
    let result = '';
    for (let i = 0; i < length; i++) {
        result += chars.charAt(Math.floor(Math.random() * chars.length));
    }
    return result;
}

// --- Setup: Create test user ---

export function setup() {
    const email = `loadtest-${randomString(8)}@test.evc-mesh.local`;
    const password = 'LoadTest123';

    // Register user.
    const registerRes = http.post(`${BASE_URL}/api/v1/auth/register`, JSON.stringify({
        email: email,
        password: password,
        name: 'Load Test User',
    }), { headers: { 'Content-Type': 'application/json' } });

    if (registerRes.status !== 201) {
        console.error(`Registration failed: ${registerRes.status} ${registerRes.body}`);
        return null;
    }

    const registerData = JSON.parse(registerRes.body);
    const token = registerData.tokens.access_token;
    const userID = registerData.user.id;

    // Get workspace.
    const wsRes = http.get(`${BASE_URL}/api/v1/workspaces`, {
        headers: headers(token),
    });

    if (wsRes.status !== 200) {
        console.error(`Get workspaces failed: ${wsRes.status}`);
        return null;
    }

    const workspaces = JSON.parse(wsRes.body);
    if (!workspaces || workspaces.length === 0) {
        console.error('No workspaces found');
        return null;
    }
    const wsID = workspaces[0].id;

    // Create project for load testing.
    const projRes = http.post(`${BASE_URL}/api/v1/workspaces/${wsID}/projects`, JSON.stringify({
        name: 'Load Test Project',
        description: 'Project for load testing',
    }), { headers: headers(token) });

    if (projRes.status !== 201) {
        console.error(`Create project failed: ${projRes.status} ${projRes.body}`);
        return null;
    }

    const project = JSON.parse(projRes.body);

    return {
        token: token,
        userID: userID,
        wsID: wsID,
        projectID: project.id,
        email: email,
    };
}

// --- Main Test Scenario ---

export default function (data) {
    if (!data || !data.token) {
        errorRate.add(1);
        return;
    }

    const hdrs = headers(data.token);
    let taskID = null;

    // 1. Create a task.
    group('Create Task', () => {
        const payload = JSON.stringify({
            title: `Load test task ${randomString(6)}`,
            description: 'Created during k6 load test',
            priority: ['low', 'medium', 'high', 'critical'][Math.floor(Math.random() * 4)],
            labels: ['load-test'],
        });

        const res = http.post(`${BASE_URL}/api/v1/projects/${data.projectID}/tasks`, payload, {
            headers: hdrs,
        });

        taskCreateDuration.add(res.timings.duration);
        taskCounter.add(1);

        const success = check(res, {
            'task created (201)': (r) => r.status === 201,
        });
        errorRate.add(!success);

        if (res.status === 201) {
            const task = JSON.parse(res.body);
            taskID = task.id;
        }
    });

    sleep(0.5);

    // 2. List tasks.
    group('List Tasks', () => {
        const res = http.get(`${BASE_URL}/api/v1/projects/${data.projectID}/tasks?page=1&page_size=20`, {
            headers: hdrs,
        });

        taskListDuration.add(res.timings.duration);

        const success = check(res, {
            'tasks listed (200)': (r) => r.status === 200,
            'has items': (r) => {
                const body = JSON.parse(r.body);
                return body.items && body.items.length > 0;
            },
        });
        errorRate.add(!success);
    });

    sleep(0.3);

    // 3. Get task by ID.
    if (taskID) {
        group('Get Task', () => {
            const res = http.get(`${BASE_URL}/api/v1/tasks/${taskID}`, {
                headers: hdrs,
            });

            check(res, {
                'task retrieved (200)': (r) => r.status === 200,
            });
        });

        sleep(0.3);

        // 4. Update task.
        group('Update Task', () => {
            const payload = JSON.stringify({
                title: `Updated task ${randomString(4)}`,
                priority: 'high',
            });

            const res = http.patch(`${BASE_URL}/api/v1/tasks/${taskID}`, payload, {
                headers: hdrs,
            });

            taskUpdateDuration.add(res.timings.duration);

            check(res, {
                'task updated (200)': (r) => r.status === 200,
            });
        });

        sleep(0.3);

        // 5. Delete task.
        group('Delete Task', () => {
            const res = http.del(`${BASE_URL}/api/v1/tasks/${taskID}`, null, {
                headers: hdrs,
            });

            check(res, {
                'task deleted (204)': (r) => r.status === 204,
            });
        });
    }

    sleep(1);
}

// --- Teardown: Clean up test data ---

export function teardown(data) {
    if (!data || !data.token) return;

    const hdrs = headers(data.token);

    // Delete project (cascading deletes tasks).
    http.del(`${BASE_URL}/api/v1/projects/${data.projectID}`, null, {
        headers: hdrs,
    });

    // Note: User cleanup would require direct DB access.
    // In a CI environment, use a dedicated test database that gets reset.
    console.log(`Load test completed. Test user: ${data.email}`);
}
