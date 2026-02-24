// k6 Load Test: WebSocket Scenario for evc-mesh
//
// Simulates concurrent WebSocket connections subscribing to project events.
//
// Usage:
//   k6 run tests/load/websocket-scenario.js
//   k6 run tests/load/websocket-scenario.js --env BASE_URL=http://localhost:8005
//
// Requirements:
//   - k6 with WebSocket support (built-in since k6 v0.31+)
//   - Running API server with WebSocket endpoint at /ws

import ws from 'k6/ws';
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// --- Configuration ---

export const options = {
    stages: [
        { duration: '30s', target: 50 },  // Ramp up to 50 concurrent WS connections
        { duration: '1m',  target: 100 }, // Sustained at 100 connections
        { duration: '30s', target: 0 },   // Ramp down
    ],
    thresholds: {
        'ws_connecting_duration': ['p(95)<1000'], // Connection under 1s
        'ws_msgs_received': ['count>0'],           // At least some messages received
    },
};

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8005';
const WS_URL = BASE_URL.replace('http://', 'ws://').replace('https://', 'wss://');

// --- Custom Metrics ---

const wsConnectionDuration = new Trend('ws_connecting_duration', true);
const wsSessionDuration = new Trend('ws_session_duration', true);
const wsMessagesReceived = new Counter('ws_msgs_received');
const wsErrors = new Rate('ws_errors');

// --- Helpers ---

function randomString(length) {
    const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
    let result = '';
    for (let i = 0; i < length; i++) {
        result += chars.charAt(Math.floor(Math.random() * chars.length));
    }
    return result;
}

// --- Setup: Create test users and project ---

export function setup() {
    const email = `ws-loadtest-${randomString(8)}@test.evc-mesh.local`;
    const password = 'LoadTest123';

    // Register.
    const registerRes = http.post(`${BASE_URL}/api/v1/auth/register`, JSON.stringify({
        email: email,
        password: password,
        name: 'WS Load Test User',
    }), { headers: { 'Content-Type': 'application/json' } });

    if (registerRes.status !== 201) {
        console.error(`Registration failed: ${registerRes.status} ${registerRes.body}`);
        return null;
    }

    const registerData = JSON.parse(registerRes.body);
    const token = registerData.tokens.access_token;

    // Get workspace.
    const wsRes = http.get(`${BASE_URL}/api/v1/workspaces`, {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`,
        },
    });

    const workspaces = JSON.parse(wsRes.body);
    if (!workspaces || workspaces.length === 0) {
        console.error('No workspaces found');
        return null;
    }
    const wsID = workspaces[0].id;

    // Create project.
    const projRes = http.post(`${BASE_URL}/api/v1/workspaces/${wsID}/projects`, JSON.stringify({
        name: 'WS Load Test Project',
    }), {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`,
        },
    });

    if (projRes.status !== 201) {
        console.error(`Create project failed: ${projRes.status}`);
        return null;
    }

    const project = JSON.parse(projRes.body);

    return {
        token: token,
        wsID: wsID,
        projectID: project.id,
        email: email,
    };
}

// --- Main WebSocket Scenario ---

export default function (data) {
    if (!data || !data.token) {
        wsErrors.add(1);
        return;
    }

    const url = `${WS_URL}/ws?token=${data.token}`;
    const sessionStart = Date.now();

    const res = ws.connect(url, {}, function (socket) {
        wsConnectionDuration.add(Date.now() - sessionStart);

        socket.on('open', () => {
            // Subscribe to project events.
            socket.send(JSON.stringify({
                type: 'subscribe',
                channel: `project:${data.projectID}`,
            }));

            // Subscribe to event bus.
            socket.send(JSON.stringify({
                type: 'subscribe',
                channel: `eventbus:project:${data.projectID}`,
            }));
        });

        socket.on('message', (msg) => {
            wsMessagesReceived.add(1);

            // Parse and validate message format.
            try {
                const parsed = JSON.parse(msg);
                check(parsed, {
                    'message has type': (m) => m.type !== undefined,
                });
            } catch (e) {
                // Some messages may not be JSON (e.g., pong frames).
            }
        });

        socket.on('error', (e) => {
            wsErrors.add(1);
            console.error(`WebSocket error: ${e}`);
        });

        socket.on('close', () => {
            wsSessionDuration.add(Date.now() - sessionStart);
        });

        // Keep the connection open for a while, sending periodic pings.
        socket.setInterval(() => {
            socket.send(JSON.stringify({ type: 'ping' }));
        }, 5000);

        // Close after a duration that varies per VU (15-45 seconds).
        const duration = 15000 + Math.random() * 30000;
        socket.setTimeout(() => {
            // Unsubscribe before closing.
            socket.send(JSON.stringify({
                type: 'unsubscribe',
                channel: `project:${data.projectID}`,
            }));
            socket.close();
        }, duration);
    });

    check(res, {
        'WebSocket connection successful': (r) => r && r.status === 101,
    });

    sleep(1);
}

// --- Teardown ---

export function teardown(data) {
    if (!data || !data.token) return;

    // Clean up project.
    http.del(`${BASE_URL}/api/v1/projects/${data.projectID}`, null, {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${data.token}`,
        },
    });

    console.log(`WebSocket load test completed. Test user: ${data.email}`);
}
