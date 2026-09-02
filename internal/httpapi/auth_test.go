package httpapi_test

import (
	"net/http"
	"testing"
)

// 共有 Bearer トークンの検査。
//
// 判断の正本は docs/adr/0020-phase2-corpus-integration-contract.md の Decision 3。
// 🔴 ここで縛るのは「掛かっていること」だけでなく「掛かっていない口が
// /healthz と /readyz の2つに限られること」でもある。認証は、抜けても
// エラーにならず、通常の利用では一切症状が出ない種類の仕組みである。

// testToken は検査に使う共有トークン。
const testToken = "s3cret-token"

// newAuthServer はトークンを設定したサーバを返す。
func newAuthServer(t *testing.T) http.Handler {
	t.Helper()

	cfg := testConfig()
	cfg.APIToken = testToken

	searcher, writer := newFakes()

	return newTestServerWith(t, cfg, newDeps(searcher, writer))
}

// TestV1RequiresBearerTokenWhenConfigured は 401 になる3経路を確かめる。
//
// 🔴 「欠落」と「不一致」と「別スキーム」を分けて見る。Authorization の解釈は
// スキーム名の比較を挟むので、方式を見ずに値だけを比べる実装だと Basic 認証の
// base64 文字列がそのままトークンとして通りうる。
func TestV1RequiresBearerTokenWhenConfigured(t *testing.T) {
	srv := newAuthServer(t)

	headers := map[string]string{
		"ヘッダが無い":        "",
		"トークンが違う":       "Bearer wrong-token",
		"別スキーム (Basic)": "Basic " + testToken,
		"スキームが無い":       testToken,
		"空の Bearer":     "Bearer ",
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			rec := doWithAuth(t, srv, request{
				method: http.MethodPost, path: "/v1/search", body: `{"org_id":1,"query":"問い"}`,
			}, header)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
			}

			// RFC 7235 は 401 に WWW-Authenticate を要求する。無いと、
			// クライアントは何を出せばよいか知る手立てが無い。
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
			}

			if code := errorCode(t, rec); code != "unauthorized" {
				t.Errorf("code = %q, want %q", code, "unauthorized")
			}
		})
	}
}

// TestUnauthorizedNeverEchoesTheToken は 401 応答にトークンが混ざらないことを見る。
//
// 期待値も、受け取った値も、長さも返さない。失敗の理由を細かく返すほど、
// 総当たりの手掛かりが増える。
func TestUnauthorizedNeverEchoesTheToken(t *testing.T) {
	srv := newAuthServer(t)

	rec := doWithAuth(t, srv, request{
		method: http.MethodPost, path: "/v1/search", body: `{"org_id":1,"query":"問い"}`,
	}, "Bearer wrong-token")

	for _, secret := range []string{testToken, "wrong-token"} {
		if contains(rec.Body.String(), secret) {
			t.Errorf("🔴 401 の応答にトークンが含まれている: %s", rec.Body.String())
		}
	}
}

// TestBearerTokenAllowsAllV1Endpoints は正しいトークンで /v1/* が通ることを見る。
//
// 🔴 全エンドポイントを1つずつ見る。middleware を掛け忘れた口があっても
// 「通る」ので、掛け忘れは検索側のテストでは一切現れない。逆に、包む場所を
// 間違えて特定の口だけ 401 になる誤りもここでしか出ない。
func TestBearerTokenAllowsAllV1Endpoints(t *testing.T) {
	srv := newAuthServer(t)

	cases := []request{
		{method: http.MethodPost, path: "/v1/search", body: `{"org_id":1,"query":"問い"}`},
		{method: http.MethodPost, path: "/v1/chunks", body: `{"org_id":1,"chunks":[` + validChunk + `]}`},
		{method: http.MethodDelete, path: "/v1/chunks/5?org_id=1", body: ""},
		{method: http.MethodDelete, path: "/v1/sources/9/chunks?org_id=1", body: ""},
		{method: http.MethodDelete, path: "/v1/documents/9/chunks?org_id=1", body: ""},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := doWithAuth(t, srv, tc, "Bearer "+testToken)
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("正しいトークンで 401 になった (%s)", rec.Body.String())
			}
		})
	}
}

// TestBearerSchemeIsCaseInsensitive は方式名の大文字小文字を区別しないことを見る。
//
// RFC 7235 が「方式名は大文字小文字を区別せずに比較する」と定めている。
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	srv := newAuthServer(t)

	rec := doWithAuth(t, srv, request{
		method: http.MethodPost, path: "/v1/search", body: `{"org_id":1,"query":"問い"}`,
	}, "bearer "+testToken)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("小文字の bearer が拒否された (%s)", rec.Body.String())
	}
}

// TestHealthEndpointsNeverRequireToken は監視の口が認証を要求しないことを見る。
//
// 🔴 トークンを要求すると、監視系から「落ちている」と「認証されていない」の
// 区別がつかなくなる。/healthz が秘匿情報を載せていないことがこの判断の前提で、
// それは healthCheck の doc と TestErrorResponseNeverCarriesConfig が守っている。
func TestHealthEndpointsNeverRequireToken(t *testing.T) {
	srv := newAuthServer(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			rec := doWithAuth(t, srv, request{method: http.MethodGet, path: path, body: ""}, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestNoTokenConfiguredMeansNoAuth は未設定なら素通しになることを見る。
//
// 🔴 既定は認証なしである（個人のローカル利用）。ここが 401 に変わると、
// recallctl も評価ハーネスも動かなくなる。逆に「未設定なら既定のトークン」に
// してしまうと、設定を忘れた全員が同じ鍵を使う状態になる。
func TestNoTokenConfiguredMeansNoAuth(t *testing.T) {
	srv := newTestServer(t) // testConfig は APIToken を空にしている

	rec := post(t, srv, "/v1/search", `{"org_id":1,"query":"問い"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// 余計なトークンを付けても素通しのままであること。認証が無効なときに
	// ヘッダの有無で挙動が変わると、設定の状態が読み取れなくなる。
	withHeader := doWithAuth(t, srv, request{
		method: http.MethodPost, path: "/v1/search", body: `{"org_id":1,"query":"問い"}`,
	}, "Bearer whatever")
	if withHeader.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", withHeader.Code, withHeader.Body.String())
	}
}
