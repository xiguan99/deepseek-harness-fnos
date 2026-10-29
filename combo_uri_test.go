package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"testing"
)

func newPR(raw string, target *url.URL) *httputil.ProxyRequest {
	in := &http.Request{Method: "GET", URL: &url.URL{}, RequestURI: raw}
	pr := &httputil.ProxyRequest{In: in, Out: in.Clone(in.Context())}
	out, _ := url.Parse(target.String())
	pr.Out.URL = out
	return pr
}

// 浏览器发出的折叠多路复用请求，经 Go 解析后必须逐字节还原成同一请求行。
func TestRestoreComboURI(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		prefix    string
		wantURI   string
	}{
		{"root 单模块", "/plugins/??@a/b/client.js&rev=1", "", "/plugins/??@a/b/client.js&rev=1"},
		{"root 多模块", "/plugins/??@a/b/client.js,@c/d/client.js&rev=2", "", "/plugins/??@a/b/client.js,@c/d/client.js&rev=2"},
		{"fngateway 单模块", "/app/deepseek-harness/fngateway/plugins/??@a/b/client.js&rev=3", fnGatewayPrefix, "/plugins/??@a/b/client.js&rev=3"},
		{"普通资源不动", "/plugins/@a/b/client.js?rev=4", "", "/plugins/@a/b/client.js?rev=4"},
	}
	for _, c := range cases {
		target, _ := url.Parse("http://127.0.0.1:2999")
		pr := newPR(c.raw, target)
		// 模拟 net/http 的解析结果：第一个 ? 之后进入 RawQuery
		if i := indexByte(c.raw, '?'); i >= 0 {
			pr.Out.URL.Path = c.raw[:i]
			pr.Out.URL.RawQuery = c.raw[i+1:]
		} else {
			pr.Out.URL.Path = c.raw
		}
		if c.prefix != "" {
			pr.Out.URL.Path = trimPrefix(pr.Out.URL.Path, c.prefix)
			pr.Out.URL.RawQuery = "" // 前缀剥离后的查询由还原逻辑重算
		}
		restoreComboURI(pr, c.prefix)
		if got := pr.Out.URL.RequestURI(); got != c.wantURI {
			t.Fatalf("%s: got %q want %q", c.name, got, c.wantURI)
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}
