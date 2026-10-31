package extsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func signed(t *testing.T, secret string, now time.Time, body string) http.Header {
	t.Helper()
	ts := strconv.FormatInt(now.Unix(), 10)
	sig, err := Sign(secret, ts, "d-1", "task.updated", []byte(body))
	require.NoError(t, err)
	h := http.Header{}
	h.Set(HeaderSignature, sig)
	h.Set(HeaderVersion, "2")
	h.Set(HeaderTimestamp, ts)
	h.Set(HeaderDelivery, "d-1")
	h.Set(HeaderEvent, "task.updated")
	return h
}

func TestCanonicalBytes(t *testing.T) {
	got := CanonicalBytes("1700000000", "d-1", "task.updated", []byte(`{"a":1}`))
	assert.Equal(t, "mesh-ext-sig-v2\n1700000000\nd-1\ntask.updated\n{\"a\":1}", string(got))
}

// The expected MAC is built here from the literal v2 layout, not from
// CanonicalBytes, so a change to the signed bytes cannot pass unnoticed.
func TestSign_MatchesIndependentHMAC(t *testing.T) {
	const ts, id, ev, body = "1700000000", "d-1", "task.updated", `{"a":1}`
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte("mesh-ext-sig-v2\n" + ts + "\n" + id + "\n" + ev + "\n" + body))
	want := "v2=" + hex.EncodeToString(m.Sum(nil))

	got, err := Sign("s3cret", ts, id, ev, []byte(body))
	require.NoError(t, err)
	assert.Equal(t, want, got)

	now := time.Unix(1700000000, 0)
	h := http.Header{}
	h.Set(HeaderSignature, want)
	h.Set(HeaderVersion, "2")
	h.Set(HeaderTimestamp, ts)
	h.Set(HeaderDelivery, id)
	h.Set(HeaderEvent, ev)
	_, err = Verifier{Secret: "s3cret", Now: func() time.Time { return now }}.Verify(h, []byte(body))
	assert.NoError(t, err)
}

// CodeQL go/allocation-size-overflow: CanonicalBytes must not size its buffer
// with an addition of lengths (the body is caller-sized). Checked on the source
// so no multi-GiB allocation is needed to see the arithmetic.
func TestCanonicalBytes_NoCapacityArithmetic(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "extsig.go", nil, 0)
	require.NoError(t, err)
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Name.Name == "CanonicalBytes" {
			fn = d
		}
	}
	require.NotNil(t, fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "make" {
			for _, a := range c.Args[1:] {
				ast.Inspect(a, func(n ast.Node) bool {
					if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.ADD {
						t.Errorf("make size uses an unchecked addition")
					}
					return true
				})
			}
		}
		return true
	})
}

func TestSign_RefusesLineBreaks(t *testing.T) {
	for _, f := range [][3]string{{"1\n2", "d", "e"}, {"1", "d\r", "e"}, {"1", "d", "e\nx"}} {
		_, err := Sign("s", f[0], f[1], f[2], nil)
		assert.Error(t, err)
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v := Verifier{Secret: "s", Now: func() time.Time { return now }}
	body := `{"event_id":"ev-1"}`

	d, err := v.Verify(signed(t, "s", now, body), []byte(body))
	require.NoError(t, err)
	assert.Equal(t, Delivery{DeliveryID: "d-1", Event: "task.updated", EventID: "ev-1"}, d)

	d, err = v.Verify(signed(t, "s", now, `{}`), []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "d-1", d.EventID, "no event_id in the body: the delivery id")

	bad := map[string]func(h http.Header){
		"wrong version":  func(h http.Header) { h.Set(HeaderVersion, "1") },
		"no version":     func(h http.Header) { h.Del(HeaderVersion) },
		"no prefix":      func(h http.Header) { h.Set(HeaderSignature, h.Get(HeaderSignature)[len(Prefix):]) },
		"missing event":  func(h http.Header) { h.Del(HeaderEvent) },
		"missing id":     func(h http.Header) { h.Del(HeaderDelivery) },
		"missing ts":     func(h http.Header) { h.Del(HeaderTimestamp) },
		"line break":     func(h http.Header) { h.Set(HeaderEvent, "a\nb") },
		"other event":    func(h http.Header) { h.Set(HeaderEvent, "task.deleted") },
		"other delivery": func(h http.Header) { h.Set(HeaderDelivery, "d-2") },
	}
	for name, m := range bad {
		h := signed(t, "s", now, body)
		m(h)
		_, verr := v.Verify(h, []byte(body))
		assert.Error(t, verr, name)
	}
	_, err = v.Verify(signed(t, "other", now, body), []byte(body))
	assert.Error(t, err, "wrong secret")

	old := now.Add(-time.Hour)
	_, err = v.Verify(signed(t, "s", old, body), []byte(body))
	assert.Error(t, err, "outside the window")
	_, err = v.Verify(signed(t, "s", now.Add(time.Hour), body), []byte(body))
	assert.Error(t, err, "in the future")
	_, err = Verifier{Secret: "s", MaxSkew: 2 * time.Hour, Now: func() time.Time { return now }}.Verify(signed(t, "s", old, body), []byte(body))
	assert.NoError(t, err, "a wider window accepts it")

	h := signed(t, "s", now, body)
	h.Set(HeaderTimestamp, "soon")
	sig, _ := Sign("s", "soon", "d-1", "task.updated", []byte(body))
	h.Set(HeaderSignature, sig)
	_, err = v.Verify(h, []byte(body))
	assert.Error(t, err, "signed but not Unix seconds")

	_, err = Verifier{Secret: "s"}.Verify(signed(t, "s", time.Now(), body), []byte(body))
	assert.NoError(t, err, "default clock")
}

func TestOnce(t *testing.T) {
	var o Once
	runs := 0
	ran, err := o.Do("a", func() error { runs++; return nil })
	assert.True(t, ran)
	assert.NoError(t, err)
	ran, err = o.Do("a", func() error { runs++; return nil })
	assert.False(t, ran)
	assert.NoError(t, err)
	assert.Equal(t, 1, runs)

	boom := errors.New("boom")
	_, err = o.Do("b", func() error { return boom })
	assert.ErrorIs(t, err, boom)
	ran, _ = o.Do("b", func() error { runs++; return nil })
	assert.True(t, ran, "a failed handler is retried")

	inner := error(nil)
	_, _ = o.Do("c", func() error { _, inner = o.Do("c", func() error { return nil }); return nil })
	assert.ErrorIs(t, inner, ErrInProgress)
}

func TestOnce_ForgetsHandledEventsAfterTTL(t *testing.T) {
	now := time.Unix(1700000000, 0)
	o := Once{TTL: time.Hour, Now: func() time.Time { return now }}
	runs := 0
	do := func(id string) { _, _ = o.Do(id, func() error { runs++; return nil }) }
	do("a")
	do("b")
	do("a")
	assert.Equal(t, 2, runs)
	assert.Len(t, o.handled, 2)

	now = now.Add(2 * time.Hour)
	do("c")
	assert.Len(t, o.handled, 1, "expired entries are dropped, memory stays bounded")
	do("a")
	assert.Equal(t, 4, runs, "an event older than the TTL is handled again")
}

func TestOnce_PanickingHandlerReleasesTheClaim(t *testing.T) {
	var o Once
	assert.Panics(t, func() { _, _ = o.Do("p", func() error { panic("boom") }) })
	ran, err := o.Do("p", func() error { return nil })
	assert.True(t, ran, "the event is retried after a panic")
	assert.NoError(t, err)
}
