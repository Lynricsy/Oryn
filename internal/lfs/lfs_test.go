package lfs

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var oidA = strings.Repeat("a", 64)

func TestParsePointer(t *testing.T) {
	const v1 = "version https://git-lfs.github.com/spec/v1\n"
	for _, tc := range []struct {
		name string
		in   string
		want Pointer
		ok   bool
	}{
		{"canonical", v1 + "oid sha256:" + oidA + "\nsize 12345\n", Pointer{oidA, 12345}, true},
		{"hawser", "version https://hawser.github.com/spec/v1\noid sha256:" + oidA + "\nsize 0\n", Pointer{oidA, 0}, true},
		{"max int64", v1 + "oid sha256:" + oidA + "\nsize 9223372036854775807\n", Pointer{oidA, 1<<63 - 1}, true},
		{"no trailing newline", v1 + "oid sha256:" + oidA + "\nsize 1", Pointer{}, false},
		{"crlf", "version https://git-lfs.github.com/spec/v1\r\noid sha256:" + oidA + "\r\nsize 1\r\n", Pointer{}, false},
		{"uppercase oid", v1 + "oid sha256:" + strings.Repeat("A", 64) + "\nsize 1\n", Pointer{}, false},
		{"short oid", v1 + "oid sha256:" + oidA[:63] + "\nsize 1\n", Pointer{}, false},
		{"leading zero size", v1 + "oid sha256:" + oidA + "\nsize 01\n", Pointer{}, false},
		{"negative size", v1 + "oid sha256:" + oidA + "\nsize -1\n", Pointer{}, false},
		{"int64 overflow", v1 + "oid sha256:" + oidA + "\nsize 9223372036854775808\n", Pointer{}, false},
		{"extension key", v1 + "ext-0-foo x\noid sha256:" + oidA + "\nsize 1\n", Pointer{}, false},
		{"extra line", v1 + "oid sha256:" + oidA + "\nsize 1\nx y\n", Pointer{}, false},
		{"other version", "version https://example.com/spec/v1\noid sha256:" + oidA + "\nsize 1\n", Pointer{}, false},
		{"1024 bytes", strings.Repeat("x", 1023) + "\n", Pointer{}, false},
		{"invalid utf8", v1 + "oid sha256:" + oidA + "\nsize 1\n\xff\n", Pointer{}, false},
		{"empty", "", Pointer{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePointer([]byte(tc.in))
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPointerRoundTrip(t *testing.T) {
	p := Pointer{OID: "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393", Size: 12345}
	got, ok := ParsePointer(p.Encode())
	require.True(t, ok)
	assert.Equal(t, p, got)
}

func TestValidOID(t *testing.T) {
	assert.True(t, ValidOID(oidA))
	assert.False(t, ValidOID(oidA+"a"))
	assert.False(t, ValidOID("../x"))
	assert.False(t, ValidOID(""))
}

func TestObjectPath(t *testing.T) {
	oid := "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"
	assert.Equal(t, filepath.Join("/r", "4d", "7a", oid), ObjectPath("/r", oid))
}

func TestBatchJSON(t *testing.T) {
	b, err := json.Marshal(BatchRequest{Operation: "download", Transfers: []string{"basic"}, Objects: []ObjectSpec{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"operation":"download","transfers":["basic"],"objects":[]}`, string(b))
	assert.Equal(t, `{"operation":"download","transfers":["basic"],"objects":[]}`, string(b))

	var resp BatchResponse
	require.NoError(t, json.Unmarshal([]byte(`{"transfer":"basic","objects":[{"oid":"`+oidA+`","size":3,
		"actions":{"download":{"href":"https://x/o","header":{"A":"b"},"expires_in":60}}},
		{"oid":"`+oidA+`","size":1,"error":{"code":404,"message":"nope"}}],"hash_algo":"sha256"}`), &resp))
	require.Len(t, resp.Objects, 2)
	assert.Equal(t, "https://x/o", resp.Objects[0].Actions["download"].Href)
	assert.Equal(t, 404, resp.Objects[1].Error.Code)
}

func TestActionExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	at, ok := (&Action{ExpiresIn: 60, ExpiresAt: "2000-01-01T00:00:00Z"}).Expiry(now)
	require.True(t, ok)
	assert.Equal(t, now.Add(time.Minute), at)

	at, ok = (&Action{ExpiresAt: "2030-01-02T03:04:05Z"}).Expiry(now)
	require.True(t, ok)
	assert.Equal(t, time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), at.UTC())

	_, ok = (&Action{}).Expiry(now)
	assert.False(t, ok)
	_, ok = (&Action{ExpiresAt: "tomorrow"}).Expiry(now)
	assert.False(t, ok)
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 403, `oryn: "no"`)
	assert.Equal(t, 403, rec.Code)
	assert.Equal(t, MediaType, rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"message":"oryn: \"no\""}`, rec.Body.String())
	assert.Equal(t, string(ErrorBody(`oryn: "no"`)), rec.Body.String())
}
