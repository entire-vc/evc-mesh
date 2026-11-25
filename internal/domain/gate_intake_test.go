package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gateIntakeValidBody = `{
  "schema": "contenthub-spark-gate-wake/v1",
  "approval_id": "3f0c2a5e-8b1d-4c7a-9e2f-1a2b3c4d5e6f",
  "task_id": 4711,
  "draft_version": 3,
  "payload_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "manifest_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "spark_project_id": 12,
  "mr_iid": 128,
  "head_sha": "0123456789abcdef0123456789abcdef01234567",
  "mr_url": "https://gitlab.example.com/entire-vc/spark/-/merge_requests/128",
  "article_url": "https://spark.entire.vc/blog/some-article"
}`

// The canonical form is frozen: receipts never expire, so a change in it turns
// every later replay of a stored approval into a 409. The literal below is
// written by hand from the contract (sorted keys, no whitespace, decimal
// integers, no HTML escaping); the hash pins it.
const (
	gateIntakeGoldenCanonical = `{"approval_id":"3f0c2a5e-8b1d-4c7a-9e2f-1a2b3c4d5e6f","article_url":"https://spark.entire.vc/blog/some-article",` +
		`"draft_version":3,"head_sha":"0123456789abcdef0123456789abcdef01234567",` +
		`"manifest_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","mr_iid":128,` +
		`"mr_url":"https://gitlab.example.com/entire-vc/spark/-/merge_requests/128",` +
		`"payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"schema":"contenthub-spark-gate-wake/v1","spark_project_id":12,"task_id":4711}`
	gateIntakeGoldenSHA = "043e8421b83f948b53f68be198dd3e7f5e2faaa5cbf3cb3933c4981e65128d90"
)

func TestGateIntake_CanonicalJSON_GoldenVector(t *testing.T) {
	r, perr := ParseGateIntakeRequest([]byte(gateIntakeValidBody))
	require.Nil(t, perr)
	assert.Equal(t, gateIntakeGoldenCanonical, string(r.CanonicalJSON()))
	assert.Equal(t, gateIntakeGoldenSHA, r.RequestSHA256())
}

func TestGateIntake_Fingerprint_IgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, perr := ParseGateIntakeRequest([]byte(gateIntakeValidBody))
	require.Nil(t, perr)
	compact := `{"task_id":4711,"mr_iid":128,"approval_id":"3f0c2a5e-8b1d-4c7a-9e2f-1a2b3c4d5e6f","draft_version":3,` +
		`"payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"manifest_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spark_project_id":12,` +
		`"head_sha":"0123456789abcdef0123456789abcdef01234567","schema":"contenthub-spark-gate-wake/v1",` +
		`"mr_url":"https:\/\/gitlab.example.com\/entire-vc\/spark\/-\/merge_requests\/128","article_url":"https://spark.entire.vc/blog/some-article"}`
	b, perr := ParseGateIntakeRequest([]byte(compact))
	require.Nil(t, perr)
	assert.Equal(t, a.RequestSHA256(), b.RequestSHA256())
	c, perr := ParseGateIntakeRequest([]byte(strings.Replace(gateIntakeValidBody, `"draft_version": 3`, `"draft_version": 4`, 1)))
	require.Nil(t, perr)
	assert.NotEqual(t, a.RequestSHA256(), c.RequestSHA256(), "a changed field is a different fingerprint")
}

func TestGateIntake_ParseRefusals(t *testing.T) {
	sub := func(old, new string) string { return strings.Replace(gateIntakeValidBody, old, new, 1) }
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		field  string
	}{
		{"not json", `hello`, 415, "unsupported_media_type", ""},
		{"array", `[1,2]`, 415, "unsupported_media_type", ""},
		{"trailing data", gateIntakeValidBody + `{}`, 415, "unsupported_media_type", ""},
		{"project_id", sub(`"task_id": 4711,`, `"task_id": 4711, "project_id": "feceae7f-5037-4fdd-bf76-d149ede06281",`), 403, GateIntakeCodeTargetFixed, ""},
		{"assignee_id", sub(`"task_id": 4711,`, `"task_id": 4711, "assignee_id": "x",`), 403, GateIntakeCodeTargetFixed, ""},
		{"labels", sub(`"task_id": 4711,`, `"task_id": 4711, "labels": ["kind:change"],`), 403, GateIntakeCodeTargetFixed, ""},
		{"priority", sub(`"task_id": 4711,`, `"task_id": 4711, "priority": "low",`), 403, GateIntakeCodeTargetFixed, ""},
		{"unknown field", sub(`"task_id": 4711,`, `"task_id": 4711, "extra": 1,`), 400, "validation_failed", "extra"},
		{"duplicate key", sub(`"task_id": 4711,`, `"task_id": 4711, "task_id": 4712,`), 400, "validation_failed", "task_id"},
		{"missing field", sub(`"mr_iid": 128,`, ``), 400, "validation_failed", "mr_iid"},
		{"schema", sub(`gate-wake/v1"`, `gate-wake/v2"`), 400, "validation_failed", "schema"},
		{"uppercase uuid", sub(`3f0c2a5e-8b1d`, `3F0C2A5E-8b1d`), 400, "validation_failed", "approval_id"},
		{"nil uuid", sub(`3f0c2a5e-8b1d-4c7a-9e2f-1a2b3c4d5e6f`, `00000000-0000-0000-0000-000000000000`), 400, "validation_failed", "approval_id"},
		{"zero task_id", sub(`"task_id": 4711`, `"task_id": 0`), 400, "validation_failed", "task_id"},
		{"float task_id", sub(`"task_id": 4711`, `"task_id": 4711.0`), 400, "validation_failed", "task_id"},
		{"exp task_id", sub(`"task_id": 4711`, `"task_id": 4.711e3`), 400, "validation_failed", "task_id"},
		{"string task_id", sub(`"task_id": 4711`, `"task_id": "4711"`), 400, "validation_failed", "task_id"},
		{"spark project", sub(`"spark_project_id": 12`, `"spark_project_id": 13`), 400, "validation_failed", "spark_project_id"},
		{"short sha256", sub(`"aaaaaaaa`, `"aaaaaaa`), 400, "validation_failed", "payload_sha256"},
		{"same hashes", sub(`bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb`, `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`), 400, "validation_failed", "manifest_sha256"},
		{"short head", sub(`01234567"`, `0123456"`), 400, "validation_failed", "head_sha"},
		{"upper head", sub(`0123456789abcdef0123`, `0123456789ABCDEF0123`), 400, "validation_failed", "head_sha"},
		{"http mr url", sub(`"https://gitlab.example.com`, `"http://gitlab.example.com`), 400, "validation_failed", "mr_url"},
		{"mr url iid", sub(`merge_requests/128"`, `merge_requests/129"`), 400, "validation_failed", "mr_url"},
		{"article host", sub(`https://spark.entire.vc/blog/`, `https://evil.example/blog/`), 400, "validation_failed", "article_url"},
		{"empty slug", sub(`blog/some-article`, `blog/`), 400, "validation_failed", "article_url"},
		{"empty slug before query", sub(`blog/some-article`, `blog/?x=1`), 400, "validation_failed", "article_url"},
		{"empty slug before fragment", sub(`blog/some-article`, `blog/#section`), 400, "validation_failed", "article_url"},
		{"escaped slash slug", sub(`blog/some-article`, `blog/%2F`), 400, "validation_failed", "article_url"},
		{"dot-dot slug", sub(`blog/some-article`, `blog/../admin`), 400, "validation_failed", "article_url"},
		{"nested slug", sub(`blog/some-article`, `blog/a/b`), 400, "validation_failed", "article_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, perr := ParseGateIntakeRequest([]byte(tc.body))
			require.Nil(t, r)
			require.NotNil(t, perr)
			assert.Equal(t, tc.status, perr.Status)
			assert.Equal(t, tc.code, perr.Code)
			if tc.field != "" {
				assert.Contains(t, perr.Fields, tc.field)
			}
		})
	}
}

func TestGateIntake_Token(t *testing.T) {
	tok, err := NewGateIntakeToken()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok, "mgi_"))
	assert.Len(t, tok, 4+43)
	assert.True(t, GateIntakeTokenWellFormed(tok))
	assert.False(t, GateIntakeTokenWellFormed(tok[:len(tok)-1]))
	assert.False(t, GateIntakeTokenWellFormed("mgi_"+strings.Repeat("!", 43)))
	other, _ := NewGateIntakeToken()
	assert.NotEqual(t, tok, other)
	assert.Len(t, HashGateIntakeToken(tok), 64)
	assert.Equal(t, tok[:12], GateIntakeTokenDisplay(tok))
}

func TestGateIntake_TitleAndDescription(t *testing.T) {
	r, perr := ParseGateIntakeRequest([]byte(gateIntakeValidBody))
	require.Nil(t, perr)
	assert.Equal(t, "Spark gate: MR !128 · approval 3f0c2a5e · v3", r.Title())
	d := r.Description(r.RequestSHA256())
	for _, f := range gateIntakeFields {
		assert.Contains(t, d, "- "+f+":", "description lists %s", f)
	}
}

func TestGateIntake_ParseRefusals_FieldShapes(t *testing.T) {
	sub := func(old, new string) string {
		require.Contains(t, gateIntakeValidBody, old)
		return strings.Replace(gateIntakeValidBody, old, new, 1)
	}
	cases := []struct {
		name, body, field string
	}{
		{"string field given a number", sub(`"head_sha": "0123456789abcdef0123456789abcdef01234567"`, `"head_sha": 7`), "head_sha"},
		{"integer beyond int64", sub(`"task_id": 4711`, `"task_id": 99999999999999999999`), "task_id"},
		{"manifest hash not hex", sub(`"manifest_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`, `"manifest_sha256": "BB"`), "manifest_sha256"},
		{"mr_url with bad mr_iid still checked as URL", sub(`"mr_iid": 128`, `"mr_iid": 0`), "mr_iid"},
		{"article_url that does not parse", sub(`"article_url": "https://spark.entire.vc/blog/some-article"`, `"article_url": "https://spark.entire.vc/blog/%zz"`), "article_url"},
		{"article_url on another host", sub(`"article_url": "https://spark.entire.vc/blog/some-article"`, `"article_url": "https://example.com/blog/x"`), "article_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, perr := ParseGateIntakeRequest([]byte(tc.body))
			require.NotNil(t, perr)
			assert.Equal(t, 400, perr.Status)
			assert.Equal(t, "validation_failed", perr.Code)
			assert.Contains(t, perr.Fields, tc.field)
			assert.Equal(t, "validation_failed: Validation failed", perr.Error())
		})
	}
	// mr_iid 0 fails on its own; the mr_url is then checked only as a URL.
	_, perr := ParseGateIntakeRequest([]byte(sub(`"mr_iid": 128`, `"mr_iid": 0`)))
	require.NotNil(t, perr)
	assert.NotContains(t, perr.Fields, "mr_url")
}

func TestGateIntake_ParseRefusals_NotOneObject(t *testing.T) {
	for name, body := range map[string]string{
		"truncated after a key":   `{"schema":`,
		"bad value":               `{"schema": tru}`,
		"missing closing brace":   `{"schema": "x"`,
		"garbage after key value": `{"schema": "x" "y"}`,
	} {
		_, perr := ParseGateIntakeRequest([]byte(body))
		require.NotNil(t, perr, name)
		assert.Equal(t, 415, perr.Status, name)
	}
}

func TestGateIntake_TokenDisplayOfShortInput(t *testing.T) {
	assert.Equal(t, "mgi_", GateIntakeTokenDisplay("mgi_"), "a string shorter than the display prefix is returned whole")
}
