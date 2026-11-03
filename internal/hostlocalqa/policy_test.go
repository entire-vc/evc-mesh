package hostlocalqa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPolicyPinsAndFrozenFiles(t *testing.T) {
	p := loadPolicy(t)
	require.Equal(t, PolicySHA256, p.SHA256)
	require.False(t, p.DefaultEnabled, "the frozen policy is default OFF")
	require.Equal(t, "\n", p.Text[len(p.Text)-1:], "the text keeps its final LF")
	sum := sha256.Sum256([]byte(p.Text))
	require.Equal(t, BriefSHA256, hex.EncodeToString(sum[:]))

	// schema.json is the frozen sender schema: every const in it must agree with the embedded policy.
	raw, err := os.ReadFile("testdata/schema.json")
	require.NoError(t, err)
	var schema struct {
		Properties map[string]struct {
			Const json.RawMessage `json:"const"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.JSONEq(t, `"`+p.SHA256+`"`, string(schema.Properties["policy_sha256"].Const))
	require.JSONEq(t, `"`+p.TextSHA256+`"`, string(schema.Properties["text_sha256"].Const))
	require.JSONEq(t, `"`+p.Candidate+`"`, string(schema.Properties["candidate"].Const))
	require.JSONEq(t, `"`+p.QAPackageSHA+`"`, string(schema.Properties["qa_package_sha256"].Const))
	require.JSONEq(t, `"`+p.OperationManifestSHA+`"`, string(schema.Properties["operation_manifest_sha256"].Const))
	require.JSONEq(t, `"`+p.SourceTaskID.String()+`"`, string(schema.Properties["source_task_id"].Const))
	var text string
	require.NoError(t, json.Unmarshal(schema.Properties["text"].Const, &text))
	require.Equal(t, p.Text, text, "schema.json text is byte-identical to brief.txt")

	// The parser accepts exactly the keys schema.json requires.
	m := sendMap(p)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, schema.Required, keys)
}

func TestSenderAllowlistIsTheFixedReceiverOnly(t *testing.T) {
	p := loadPolicy(t)
	require.Equal(t, 1, len(p.AllowedSenderAgents))
	require.True(t, p.SenderAllowed(p.AgentID))
}

func TestParseSendAcceptsTheFrozenEnvelope(t *testing.T) {
	p := loadPolicy(t)
	e, err := p.ParseSend(enc(t, sendMap(p)))
	require.NoError(t, err)
	require.Equal(t, Capability, e.Capability)
	require.EqualValues(t, 1, e.ExpectedRevision)
	require.Equal(t, "native-1", e.Target.SessionID)
}

func TestParseSendRefusals(t *testing.T) {
	p := loadPolicy(t)
	mut := func(f func(m map[string]any)) []byte {
		m := sendMap(p)
		f(m)
		return enc(t, m)
	}
	cases := map[string][]byte{
		"command":          mut(func(m map[string]any) { m["command"] = "ls" }),
		"argv":             mut(func(m map[string]any) { m["argv"] = []string{"ls"} }),
		"cwd":              mut(func(m map[string]any) { m["cwd"] = "/" }),
		"tool":             mut(func(m map[string]any) { m["tool"] = "bash" }),
		"url":              mut(func(m map[string]any) { m["url"] = "https://x" }),
		"nested target":    mut(func(m map[string]any) { m["target"].(map[string]any)["extra"] = 1 }),
		"nested producer":  mut(func(m map[string]any) { m["producer"].(map[string]any)["extra"] = 1 }),
		"missing target":   mut(func(m map[string]any) { delete(m, "target") }),
		"missing producer": mut(func(m map[string]any) { delete(m["producer"].(map[string]any), "tty") }),
		"legacy cap":       mut(func(m map[string]any) { m["capability"] = "readonly_metadata" }),
		"schema 2":         mut(func(m map[string]any) { m["schema_version"] = 2 }),
		"schema bool":      mut(func(m map[string]any) { m["schema_version"] = true }),
		"text byte":        mut(func(m map[string]any) { m["text"] = p.Text + " " }),
		"text no LF":       mut(func(m map[string]any) { m["text"] = p.Text[:len(p.Text)-1] }),
		"text hash":        mut(func(m map[string]any) { m["text_sha256"] = sum([]byte("not the brief")) }),
		"policy hash":      mut(func(m map[string]any) { m["policy_sha256"] = p.TextSHA256 }),
		"package":          mut(func(m map[string]any) { m["qa_package_sha256"] = p.SHA256 }),
		"manifest":         mut(func(m map[string]any) { m["operation_manifest_sha256"] = p.SHA256 }),
		"candidate":        mut(func(m map[string]any) { m["candidate"] = "0000000000000000000000000000000000000000" }),
		"artifact":         mut(func(m map[string]any) { m["artifact_id"] = 1 }),
		"source task":      mut(func(m map[string]any) { m["source_task_id"] = fxRequest }),
		"rev missing":      mut(func(m map[string]any) { delete(m, "expected_admission_revision") }),
		"rev zero":         mut(func(m map[string]any) { m["expected_admission_revision"] = 0 }),
		"rev bool":         mut(func(m map[string]any) { m["expected_admission_revision"] = true }),
		"rev string":       mut(func(m map[string]any) { m["expected_admission_revision"] = "1" }),
		"rev float":        mut(func(m map[string]any) { m["expected_admission_revision"] = 1.5 }),
		"rev negative":     mut(func(m map[string]any) { m["expected_admission_revision"] = -1 }),
		"expires bool":     mut(func(m map[string]any) { m["expires"] = true }),
		"expires string":   mut(func(m map[string]any) { m["expires"] = "4102444800" }),
		"pid bool":         mut(func(m map[string]any) { m["producer"].(map[string]any)["producer_pid"] = true }),
		"pid zero":         mut(func(m map[string]any) { m["producer"].(map[string]any)["producer_pid"] = 0 }),
		"transport":        mut(func(m map[string]any) { m["producer"].(map[string]any)["transport"] = "tmux" }),
		"tty":              mut(func(m map[string]any) { m["producer"].(map[string]any)["tty"] = "/dev/null" }),
		"uppercase uuid":   mut(func(m map[string]any) { m["request_id"] = "99999999-8888-4777-8666-55555555555F" }),
		"nil uuid":         mut(func(m map[string]any) { m["receiver_id"] = "00000000-0000-0000-0000-000000000000" }),
		"empty session":    mut(func(m map[string]any) { m["target"].(map[string]any)["session_id"] = "" }),
		"long host":        mut(func(m map[string]any) { m["target"].(map[string]any)["host_id"] = string(make([]byte, 129)) }),
		"not an object":    []byte(`[]`),
		"empty":            []byte(``),
		"trailing":         append(enc(t, sendMap(p)), []byte(` {}`)...),
		"duplicate key":    []byte(`{"request_id":"` + fxRequest + `","request_id":"` + fxRequest + `"}`),
		"case variant key": []byte(`{"Text":"x"}`),
	}
	for name, body := range cases {
		_, err := p.ParseSend(body)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	// Identity fields are shape-checked only: a different workspace/agent/hostname parses and is
	// refused by the caller (Admitted) with the same answer as any other identity mismatch.
	for _, f := range []string{"workspace_id", "agent_id", "runner_id"} {
		m := sendMap(p)
		m["target"].(map[string]any)[f] = fxRequest
		e, err := p.ParseSend(enc(t, m))
		require.NoError(t, err, f)
		if f != "runner_id" {
			require.False(t, p.Admitted(e.Target), f)
		}
	}
	m := sendMap(p)
	m["target"].(map[string]any)["hostname"] = "other.example.invalid"
	e, err := p.ParseSend(enc(t, m))
	require.NoError(t, err)
	require.False(t, p.Admitted(e.Target))
}

func TestDuplicateKeyInsideNestedObjectIsRefused(t *testing.T) {
	p := loadPolicy(t)
	body := string(enc(t, sendMap(p)))
	body = body[:len(body)-1] + `,"target":{}}`
	_, err := p.ParseSend([]byte(body))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestParseRegistration(t *testing.T) {
	p := loadPolicy(t)
	r, err := p.ParseRegistration(enc(t, regMap(p)))
	require.NoError(t, err)
	require.Equal(t, []string{p.AgentID.String()}, []string{r.Senders[0].String()})
	bad := map[string]func(m map[string]any){
		"extra":      func(m map[string]any) { m["runner_id"] = fxRunner },
		"capability": func(m map[string]any) { m["capability"] = "readonly_metadata" },
		"policy":     func(m map[string]any) { m["policy_sha256"] = p.TextSHA256 },
		"no senders": func(m map[string]any) { m["allowed_sender_agent_ids"] = []string{} },
		"foreign":    func(m map[string]any) { m["allowed_sender_agent_ids"] = []string{fxRequest} },
		"duplicate": func(m map[string]any) {
			m["allowed_sender_agent_ids"] = []string{p.AgentID.String(), p.AgentID.String()}
		},
		"not a string": func(m map[string]any) { m["allowed_sender_agent_ids"] = []int{1} },
		"expires bool": func(m map[string]any) { m["expires"] = true },
		"nested":       func(m map[string]any) { m["producer"].(map[string]any)["x"] = 1 },
	}
	for name, f := range bad {
		m := regMap(p)
		f(m)
		_, err := p.ParseRegistration(enc(t, m))
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

func TestParseCheckAndAck(t *testing.T) {
	p := loadPolicy(t)
	_, err := p.ParseCheck(enc(t, checkMap(p)))
	require.NoError(t, err)
	for name, f := range map[string]func(m map[string]any){
		"token in body": func(m map[string]any) { m["token"] = "x" },
		"text in body":  func(m map[string]any) { m["text"] = p.Text },
		"legacy cap":    func(m map[string]any) { m["capability"] = "readonly_metadata" },
		"rev bool":      func(m map[string]any) { m["admission_revision"] = true },
		"no delivery":   func(m map[string]any) { delete(m, "delivery_id") },
	} {
		m := checkMap(p)
		f(m)
		_, cerr := p.ParseCheck(enc(t, m))
		require.ErrorIs(t, cerr, ErrInvalid, name)
	}

	for _, st := range []string{"received", "acknowledged"} {
		b, aerr := p.ParseAck(enc(t, ackMap(p, st)))
		require.NoError(t, aerr, st)
		require.Equal(t, st, b.State)
	}
	m := ackMap(p, "failed")
	m["error_code"] = "paste_failed"
	_, err = p.ParseAck(enc(t, m))
	require.NoError(t, err)
	m = ackMap(p, "acknowledged")
	m["result_comment_url"] = "https://mesh.example.invalid/t/x#c"
	_, err = p.ParseAck(enc(t, m))
	require.NoError(t, err)
	for name, f := range map[string]func(m map[string]any){
		"running":         func(m map[string]any) { m["state"] = "running" },
		"queued":          func(m map[string]any) { m["state"] = "queued" },
		"failed no code":  func(m map[string]any) { m["state"] = "failed" },
		"code on success": func(m map[string]any) { m["error_code"] = "x" },
		"bad code":        func(m map[string]any) { m["state"], m["error_code"] = "failed", "Bad Code" },
		"http url":        func(m map[string]any) { m["result_comment_url"] = "http://x/y" },
		"userinfo url":    func(m map[string]any) { m["result_comment_url"] = "https://u:p@x/y" },
		"no observed":     func(m map[string]any) { delete(m, "observed_at") },
		"local time":      func(m map[string]any) { m["observed_at"] = "2026-11-03T03:00:00+03:00" },
		"epoch":           func(m map[string]any) { m["observed_at"] = 1 },
		"transcript":      func(m map[string]any) { m["transcript"] = "x" },
	} {
		m := ackMap(p, "acknowledged")
		f(m)
		_, err := p.ParseAck(enc(t, m))
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

func TestExpiryError(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	require.Equal(t, "expired", ExpiryError(now.Unix(), now))
	require.Equal(t, "expired", ExpiryError(now.Unix()-1, now))
	require.Equal(t, "", ExpiryError(now.Unix()+1, now))
	require.Equal(t, "", ExpiryError(now.Unix()+MaxExpirySeconds, now))
	require.Equal(t, "expiry_too_far", ExpiryError(now.Unix()+MaxExpirySeconds+1, now))
}

// Every case of the frozen conformance.json is mapped to the test that proves it
// on the backend, or to the receiver/runner side that owns it. A new case in the
// file fails here until it is mapped.
var conformanceCoverage = map[string]string{
	"exact":              "handler: TestHostLocalQAAssignmentFlow/exact",
	"legacy-rejects-qa":  "handler: TestHostLocalQAAssignmentFlow/legacy-rejects-qa",
	"qa-rejects-legacy":  "handler: TestHostLocalQAAssignmentFlow/qa-rejects-legacy",
	"unknown-fields":     "handler: TestHostLocalQAAssignmentFlow/unknown-fields",
	"hash":               "handler: TestHostLocalQAAssignmentFlow/hash",
	"identity":           "handler: TestHostLocalQAAssignmentFlow/identity",
	"producer":           "handler: TestHostLocalQAAssignmentFlow/producer (backend pin); local re-verification belongs to the receiver",
	"revision":           "handler: TestHostLocalQAAssignmentFlow/revision",
	"expired":            "handler: TestHostLocalQAAssignmentFlow/expired",
	"revoked":            "handler: TestHostLocalQAAssignmentFlow/revoked",
	"timeout":            "handler: TestHostLocalQAAssignmentFlow/check (malformed and mismatched checks refuse); the no-cached-allow client behaviour belongs to the receiver",
	"duplicate":          "handler: TestHostLocalQAAssignmentFlow/duplicate",
	"conflict":           "handler: TestHostLocalQAAssignmentFlow/conflict",
	"uncertain":          "handler: TestHostLocalQAAssignmentFlow/revoked (received then revoked is uncertain, never listed again); journal replay belongs to the receiver",
	"concurrency":        "receiver: one shared lock and ledger live in evc-mesh-runner; backend serialisation is TestHostLocalQAAssignmentFlow/races",
	"disable":            "handler: TestHostLocalQAAssignmentFlow/disable",
	"ack-is-not-install": "handler: TestHostLocalQAAssignmentFlow/ack-is-not-install",
	"mutation-guard":     "handler: TestHostLocalQAAssignmentFlow/check; the guard call before each mutation belongs to the receiver and Receiver's reviewed integration",
	"mini-readback":      "receiver/Receiver: install receipt and Mini readback are source-owned, not backend",
}

func TestConformanceCasesAreAllMapped(t *testing.T) {
	raw, err := os.ReadFile("testdata/conformance.json")
	require.NoError(t, err)
	var c struct {
		Cases []struct {
			ID string `json:"id"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &c))
	require.NotEmpty(t, c.Cases)
	seen := map[string]bool{}
	for _, cs := range c.Cases {
		seen[cs.ID] = true
		require.Contains(t, conformanceCoverage, cs.ID, "conformance case %q has no mapped test", cs.ID)
	}
	for id := range conformanceCoverage {
		require.True(t, seen[id], "%q is mapped but no longer in conformance.json", id)
	}
}
