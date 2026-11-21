package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitCredential(t *testing.T) {
	const hit = "username=oryn\npassword=odt_x\n"
	login := credentials{URL: "https://oryn.example.com", Token: "odt_x"}
	cases := []struct {
		name    string
		op      string
		in      string
		cred    credentials
		credErr error
		want    string
	}{
		{"同主机", "get", "protocol=https\nhost=oryn.example.com\n\n", login, nil, hit},
		{"主机大小写", "get", "protocol=https\nhost=ORYN.example.com\n\n", login, nil, hit},
		{"多余字段与 EOF", "get", "protocol=https\nhost=oryn.example.com\npath=git/p.git\nwwwauth[]=Basic realm=\"Oryn\"", login, nil, hit},
		{"多了端口", "get", "protocol=https\nhost=oryn.example.com:8443\n\n", login, nil, ""},
		{"协议不同", "get", "protocol=http\nhost=oryn.example.com\n\n", login, nil, ""},
		{"别的主机", "get", "protocol=https\nhost=github.com\n\n", login, nil, ""},
		{"缺 host", "get", "protocol=https\n\n", login, nil, ""},
		{"登录带端口", "get", "protocol=https\nhost=h:8443\n\n", credentials{URL: "https://h:8443", Token: "odt_x"}, nil, hit},
		{"读凭据失败", "get", "protocol=https\nhost=oryn.example.com\n\n", credentials{}, errors.New("尚未登录"), ""},
		{"令牌含换行", "get", "protocol=https\nhost=oryn.example.com\n\n", credentials{URL: login.URL, Token: "odt_x\nusername=evil"}, nil, ""},
		{"store", "store", "protocol=https\nhost=oryn.example.com\nusername=oryn\npassword=odt_x\n\n", login, nil, ""},
		{"erase", "erase", "protocol=https\nhost=oryn.example.com\n\n", login, nil, ""},
		{"未知操作", "frob", "protocol=https\nhost=oryn.example.com\n\n", login, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, gitCredential(tc.op, strings.NewReader(tc.in), &out, tc.cred, tc.credErr))
			assert.Equal(t, tc.want, out.String())
		})
	}
}
