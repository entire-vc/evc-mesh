package domain

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture1235 is the request of Spark!1235 from the contract (LA5) and from
// ch-dev-2's request.
func fixture1235Query() url.Values {
	return url.Values{
		"project":             {"entire-vc/evc-spark"},
		"mr_iid":              {"1235"},
		"head_sha":            {"350b837f334daa6b2bbd3f3f5b520e142449a2bc"},
		"lead_gitlab_id":      {"6"},
		"review_note_id":      {"50651"},
		"lead_accept_note_id": {"50661"},
		"approval_id":         {"9835e466-1560-46b7-9fbe-0f80631e0b81"},
		"approval_version":    {"5"},
		"payload_sha256":      {"64788c002a6d68c2e50ee600b6c781f3a771ff678ff30d0104360841dc4b0813"},
		// Unsorted on purpose: the server sorts.
		"evidence": {"comment:074fb350-74f6-4136-a9d1-9ec10506ecdc/bc203833-f3d0-484b-9f54-f232270bfa6b",
			"artifact:4a3ace64-dc01-4537-8416-b3b23068b800/76b67513-81f2-47c8-944f-931ad4002fde"},
	}
}

const fixture1235SHA = "61a6ec8206a3beddf9f8fa2829beb7cbfc06109dabf0307110015481841c5205"

const fixture1235Canonical = `{"approval_id":"9835e466-1560-46b7-9fbe-0f80631e0b81","approval_version":5,"evidence":["artifact:4a3ace64-dc01-4537-8416-b3b23068b800/76b67513-81f2-47c8-944f-931ad4002fde","comment:074fb350-74f6-4136-a9d1-9ec10506ecdc/bc203833-f3d0-484b-9f54-f232270bfa6b"],"head_sha":"350b837f334daa6b2bbd3f3f5b520e142449a2bc","lead_accept_note_id":50661,"lead_gitlab_id":6,"mr_iid":1235,"payload_sha256":"64788c002a6d68c2e50ee600b6c781f3a771ff678ff30d0104360841dc4b0813","project":"entire-vc/evc-spark","review_note_id":50651}`

// The golden vector: the canonical JSON and request_sha256 of the fixture are
// exactly what CH computed (json.dumps sort_keys, compact separators).
func TestLeadAcceptRequestGoldenVector(t *testing.T) {
	r, bad := ParseLeadAcceptQuery(fixture1235Query().Encode())
	require.Nil(t, bad)
	assert.Equal(t, fixture1235Canonical, string(r.CanonicalJSON()))
	assert.Equal(t, fixture1235SHA, r.SHA256())

	// The request file of `issue --kind reader` gives the same binding.
	fr, bad := ParseLeadAcceptRequestJSON([]byte(fixture1235Canonical))
	require.Nil(t, bad)
	assert.Equal(t, fixture1235SHA, fr.SHA256())
}

func TestParseLeadAcceptQueryRefusals(t *testing.T) {
	mod := func(f func(v url.Values)) string {
		v := fixture1235Query()
		f(v)
		return v.Encode()
	}
	cases := map[string]struct {
		q     string
		field string
	}{
		"missing param":      {mod(func(v url.Values) { v.Del("mr_iid") }), "mr_iid"},
		"unknown param":      {mod(func(v url.Values) { v.Set("limit", "5") }), "limit"},
		"repeated param":     {mod(func(v url.Values) { v["mr_iid"] = []string{"1235", "1235"} }), "mr_iid"},
		"short head":         {mod(func(v url.Values) { v.Set("head_sha", "350b837f") }), "head_sha"},
		"uppercase head":     {mod(func(v url.Values) { v.Set("head_sha", strings.ToUpper("350b837f334daa6b2bbd3f3f5b520e142449a2bc")) }), "head_sha"},
		"leading zero":       {mod(func(v url.Values) { v.Set("mr_iid", "01235") }), "mr_iid"},
		"zero":               {mod(func(v url.Values) { v.Set("lead_gitlab_id", "0") }), "lead_gitlab_id"},
		"negative":           {mod(func(v url.Values) { v.Set("approval_version", "-5") }), "approval_version"},
		"uppercase uuid":     {mod(func(v url.Values) { v.Set("approval_id", "9835E466-1560-46B7-9FBE-0F80631E0B81") }), "approval_id"},
		"short approval":     {mod(func(v url.Values) { v.Set("approval_id", "9835e466") }), "approval_id"},
		"nil approval":       {mod(func(v url.Values) { v.Set("approval_id", uuid.Nil.String()) }), "approval_id"},
		"notes out of order": {mod(func(v url.Values) { v.Set("lead_accept_note_id", "50651") }), "lead_accept_note_id"},
		"short evidence":     {mod(func(v url.Values) { v["evidence"] = []string{"comment:074fb350/bc203833"} }), "evidence"},
		"marker evidence":    {mod(func(v url.Values) { v["evidence"] = []string{"76b67513"} }), "evidence"},
		"duplicate evidence": {mod(func(v url.Values) { v["evidence"] = []string{v["evidence"][0], v["evidence"][0]} }), "evidence"},
		"too many evidence": {mod(func(v url.Values) {
			v["evidence"] = append(v["evidence"], v["evidence"][0][:9]+uuid.NewString()+"/"+uuid.NewString(), "comment:"+uuid.NewString()+"/"+uuid.NewString(), "comment:"+uuid.NewString()+"/"+uuid.NewString())
		}), "evidence"},
		"no evidence":            {mod(func(v url.Values) { v.Del("evidence") }), "evidence"},
		"project not a path":     {mod(func(v url.Values) { v.Set("project", "evc-spark") }), "project"},
		"short payload":          {mod(func(v url.Values) { v.Set("payload_sha256", "64788c00") }), "payload_sha256"},
		"evidence kind":          {mod(func(v url.Values) { v["evidence"] = []string{"task:" + uuid.NewString() + "/" + uuid.NewString()} }), "evidence"},
		"unparsable query":       {"mr_iid=%zz", "query"},
		"fractional number":      {mod(func(v url.Values) { v.Set("mr_iid", "1235.0") }), "mr_iid"},
		"plus sign number":       {mod(func(v url.Values) { v.Set("mr_iid", "+1235") }), "mr_iid"},
		"whitespace in head sha": {mod(func(v url.Values) { v.Set("head_sha", " 350b837f334daa6b2bbd3f3f5b520e142449a2bc") }), "head_sha"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, bad := ParseLeadAcceptQuery(tc.q)
			assert.Nil(t, r)
			require.NotNil(t, bad)
			assert.Contains(t, bad, tc.field)
		})
	}
}

func TestParseLeadAcceptRequestJSONRefusals(t *testing.T) {
	var base map[string]any
	require.NoError(t, json.Unmarshal([]byte(fixture1235Canonical), &base))
	mk := func(f func(m map[string]any)) []byte {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	for name, body := range map[string][]byte{
		"string number":  mk(func(m map[string]any) { m["mr_iid"] = "1235" }),
		"float number":   []byte(strings.Replace(fixture1235Canonical, `"mr_iid":1235`, `"mr_iid":1235.0`, 1)),
		"missing field":  mk(func(m map[string]any) { delete(m, "review_note_id") }),
		"extra field":    mk(func(m map[string]any) { m["workspace_id"] = uuid.NewString() }),
		"evidence str":   mk(func(m map[string]any) { m["evidence"] = "comment:x/y" }),
		"empty evidence": mk(func(m map[string]any) { m["evidence"] = []string{} }),
		"duplicate key":  []byte(strings.Replace(fixture1235Canonical, `"mr_iid":1235`, `"mr_iid":1235,"mr_iid":1236`, 1)),
		"not an object":  []byte(`[1]`),
		"trailing data":  []byte(fixture1235Canonical + `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			r, bad := ParseLeadAcceptRequestJSON(body)
			assert.Nil(t, r)
			assert.NotEmpty(t, bad)
		})
	}
}

func okAttestation() map[string]any {
	return map[string]any{
		"schema":                         LeadAcceptAttestationSchema,
		"observation_id":                 uuid.NewString(),
		"observed_at":                    time.Now().UTC().Format(time.RFC3339Nano),
		"ok":                             true,
		"git_commit":                     "7eea41f1ee64bd0146f4706b8ce90488c60bb318",
		"origin_main_commit":             "7eea41f1ee64bd0146f4706b8ce90488c60bb318",
		"registry_last_commit":           "6928e675bb1632fd11a4f248d14227055cd5a459",
		"registry_blob_sha1":             "9af06fab161ffa7e1f88400775e50de363717b9b",
		"origin_registry_blob_sha1":      "9af06fab161ffa7e1f88400775e50de363717b9b",
		"registry_sha256":                "0924c523f43aab5f11d1680df15644668becb7015b5c37bb813079bc31ea921f",
		"mirror_policy_blob_sha1":        "de103142a1357d2891435e5a682c7487242bd76c",
		"origin_mirror_policy_blob_sha1": "de103142a1357d2891435e5a682c7487242bd76c",
		"mirror_policy_sha256":           "1428816e70a798596f3baad25821d089a7b897441939d71bef51de88eae46626",
		"gate_code_sha256":               "144dd786e602aa449a29ac09b92e7fe75b0764efbc6b6e1c370a7a917e77cadc",
		"worktree_clean":                 true,
		"in_sync":                        true,
		"projects": map[string]any{"entire-vc/evc-spark": map[string]any{
			"lead_accept_allowed": true, "policy_reason": "",
			"leads": []any{map[string]any{"username": "howard", "gitlab_id": 6, "lane": "Howard"}}}},
	}
}

func TestParseLeadAcceptAttestation(t *testing.T) {
	enc := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	a, bad := ParseLeadAcceptAttestation(enc(okAttestation()))
	require.Nil(t, bad)
	assert.True(t, a.InSync())
	assert.Equal(t, int64(6), a.Projects[LeadAcceptProject].Leads[0].GitlabID)

	dirty := okAttestation()
	dirty["worktree_clean"] = false
	dirty["in_sync"] = false
	a, bad = ParseLeadAcceptAttestation(enc(dirty))
	require.Nil(t, bad)
	assert.False(t, a.InSync(), "a dirty gate checkout is never in sync")
	behind := okAttestation()
	behind["origin_registry_blob_sha1"] = strings.Repeat("1", 40)
	behind["in_sync"] = false
	a, bad = ParseLeadAcceptAttestation(enc(behind))
	require.Nil(t, bad)
	assert.False(t, a.InSync(), "a registry blob differing from origin/main is not in sync")
	mpBehind := okAttestation()
	mpBehind["origin_mirror_policy_blob_sha1"] = strings.Repeat("2", 40)
	mpBehind["in_sync"] = false
	a, bad = ParseLeadAcceptAttestation(enc(mpBehind))
	require.Nil(t, bad)
	assert.False(t, a.InSync())

	fail := map[string]any{"schema": LeadAcceptAttestationSchema, "observation_id": uuid.NewString(),
		"observed_at": time.Now().UTC().Format(time.RFC3339), "ok": false, "error_step": "fetch", "error": "git fetch timed out"}
	a, bad = ParseLeadAcceptAttestation(enc(fail))
	require.Nil(t, bad)
	assert.False(t, a.OK)
	assert.False(t, a.InSync())

	refuse := map[string]func(m map[string]any){
		"other project": func(m map[string]any) {
			m["projects"] = map[string]any{"entire-vc/other": m["projects"].(map[string]any)["entire-vc/evc-spark"]}
		},
		"two projects":          func(m map[string]any) { m["projects"].(map[string]any)["entire-vc/other"] = map[string]any{} },
		"missing blob":          func(m map[string]any) { delete(m, "origin_registry_blob_sha1") },
		"unknown field":         func(m map[string]any) { m["registry_origin_blob_sha1"] = strings.Repeat("1", 40) },
		"missing in_sync":       func(m map[string]any) { delete(m, "in_sync") },
		"in_sync not bool":      func(m map[string]any) { m["in_sync"] = "true" },
		"in_sync claims true":   func(m map[string]any) { m["worktree_clean"] = false },
		"in_sync claims false":  func(m map[string]any) { m["in_sync"] = false },
		"bad schema":            func(m map[string]any) { m["schema"] = "v0" },
		"not utc":               func(m map[string]any) { m["observed_at"] = "2026-11-21T11:00:00+03:00" },
		"uppercase observation": func(m map[string]any) { m["observation_id"] = strings.ToUpper(uuid.NewString()) },
		"short sha":             func(m map[string]any) { m["registry_sha256"] = "0924c523" },
		"lead without id": func(m map[string]any) {
			m["projects"] = map[string]any{"entire-vc/evc-spark": map[string]any{"lead_accept_allowed": true, "policy_reason": "",
				"leads": []any{map[string]any{"username": "howard", "lane": "Howard"}}}}
		},
		"repeated lead": func(m map[string]any) {
			l := map[string]any{"username": "howard", "gitlab_id": 6, "lane": "Howard"}
			m["projects"] = map[string]any{"entire-vc/evc-spark": map[string]any{"lead_accept_allowed": true, "policy_reason": "", "leads": []any{l, l}}}
		},
	}
	for name, f := range refuse {
		t.Run(name, func(t *testing.T) {
			m := okAttestation()
			f(m)
			a, bad := ParseLeadAcceptAttestation(enc(m))
			assert.Nil(t, a)
			assert.NotEmpty(t, bad)
		})
	}
	for name, f := range map[string]func(m map[string]any){
		"fail with data":   func(m map[string]any) { m["git_commit"] = strings.Repeat("a", 40) },
		"fail bad step":    func(m map[string]any) { m["error_step"] = "network" },
		"fail no error":    func(m map[string]any) { delete(m, "error") },
		"fail multiline":   func(m map[string]any) { m["error"] = "a\nb" },
		"fail with leads":  func(m map[string]any) { m["projects"] = map[string]any{} },
		"fail ok not bool": func(m map[string]any) { m["ok"] = "false" },
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range fail {
				m[k] = v
			}
			f(m)
			a, bad := ParseLeadAcceptAttestation(enc(m))
			assert.Nil(t, a)
			assert.NotEmpty(t, bad)
		})
	}
}

// The gate-intake card description is trusted only when its fields hash to
// the receipt's request_sha256.
func TestGateIntakeRequestFromDescription(t *testing.T) {
	gi := &GateIntakeRequest{Schema: GateIntakeSchemaContentHub, ApprovalID: uuid.New(), TaskID: 4711, DraftVersion: 5,
		PayloadSHA256: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64), SparkProjectID: 12, MRIID: 1235,
		HeadSHA: strings.Repeat("c", 40), MRURL: "https://gitlab.example.com/entire-vc/evc-spark/-/merge_requests/1235",
		ArticleURL: "https://spark.entire.vc/blog/x"}
	sha := gi.RequestSHA256()
	desc := gi.Description(sha)
	got, ok := GateIntakeRequestFromDescription(desc, sha)
	require.True(t, ok)
	assert.Equal(t, gi, got)

	_, ok = GateIntakeRequestFromDescription(strings.Replace(desc, "- draft_version: 5", "- draft_version: 6", 1), sha)
	assert.False(t, ok, "an edited field no longer hashes to the receipt")
	_, ok = GateIntakeRequestFromDescription(strings.Replace(desc, "- mr_iid: 1235\n", "", 1), sha)
	assert.False(t, ok, "a missing line is unverifiable")
	_, ok = GateIntakeRequestFromDescription(desc, strings.Repeat("0", 64))
	assert.False(t, ok, "another receipt hash")
	_, ok = GateIntakeRequestFromDescription(desc+"\n- mr_iid: 1235\n", sha)
	assert.False(t, ok, "a repeated line is unverifiable")
	_, ok = GateIntakeRequestFromDescription("", sha)
	assert.False(t, ok, "a deleted card is unverifiable")
}

func TestLeadAcceptTokens(t *testing.T) {
	r, err := NewLeadAcceptToken(LeadAcceptKindReader)
	require.NoError(t, err)
	p, err := NewLeadAcceptToken(LeadAcceptKindPublisher)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(r, "mlr_") && len(r) == 47)
	assert.True(t, strings.HasPrefix(p, "mlp_") && len(p) == 47)
	assert.True(t, LeadAcceptTokenWellFormed(r))
	assert.True(t, LeadAcceptTokenWellFormed(p))
	assert.Equal(t, LeadAcceptKindReader, LeadAcceptTokenKind(r))
	assert.Equal(t, LeadAcceptKindPublisher, LeadAcceptTokenKind(p))
	assert.False(t, LeadAcceptTokenWellFormed("mgi_"+r[4:]))
	assert.False(t, LeadAcceptTokenWellFormed(r[:46]))
	assert.False(t, LeadAcceptTokenWellFormed("mlr_"+strings.Repeat("!", 43)))
}

// context_sha256 is pinned on a fixed context: a change in what it covers or
// how it is encoded is a deliberate contract change.
func TestLeadAcceptContextSHA256Golden(t *testing.T) {
	r, bad := ParseLeadAcceptQuery(fixture1235Query().Encode())
	require.Nil(t, bad)
	upd := "2026-11-21T06:50:56.823708Z"
	c := &LeadAcceptContext{
		Request: r,
		Lead:    LeadAcceptLeadOut{Username: "howard", GitlabID: 6, Lane: "Howard", Status: "active"},
		Scope:   LeadAcceptScopeOut{Project: LeadAcceptProject, LeadAcceptAllowed: true},
		Registry: LeadAcceptRegistryOut{GitCommit: "7eea41f1ee64bd0146f4706b8ce90488c60bb318",
			RegistrySHA256:     "0924c523f43aab5f11d1680df15644668becb7015b5c37bb813079bc31ea921f",
			MirrorPolicySHA256: "1428816e70a798596f3baad25821d089a7b897441939d71bef51de88eae46626", InSync: true,
			ObservationID: uuid.NewString(), ObservedAt: time.Now().String()}, // not covered
		Evidence: []LeadAcceptEvidenceOut{
			{Ref: r.Evidence[0], Author: LeadAcceptAuthor{Type: "agent", ID: "e9f4bcb3-fc32-4362-b882-82cff1252277", Name: "Carl"},
				CreatedAt: "2026-11-21T06:55:15.343994Z", SHA256: "9204f00ed38f601dd27bb2ebd2213a56a4739d22c920d3f8f4c652196e0c2802",
				Mentions: LeadAcceptMentions{true, true, true}},
			{Ref: r.Evidence[1], Author: LeadAcceptAuthor{Type: "user", ID: "11111111-1111-4111-8111-111111111111", Name: "Alex"},
				CreatedAt: upd, UpdatedAt: &upd, SHA256: "0763b1afa07da04109b2c15071d6e44cdad0db5373a4f26ce8ed3b80214049d7"},
		},
		ApprovalBinding: LeadAcceptApprovalBinding{ApprovalID: r.ApprovalID, ApprovalVersion: 5, PayloadSHA256: r.PayloadSHA256, GateIntakeReceipt: "none"},
	}
	sum, err := c.ComputeContextSHA256()
	require.NoError(t, err)
	// Independently computed (python3 json.dumps(sort_keys=True,
	// separators=(",", ":"), ensure_ascii=False)) over the same object.
	assert.Equal(t, leadAcceptContextGolden, sum)
}

const leadAcceptContextGolden = "ed3a326cdbd4262c18d29d68882af30f64285821549c8c0a026ac122d97d2af6"
