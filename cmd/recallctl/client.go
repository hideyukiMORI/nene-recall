package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// client は Recall サーバへの HTTP クライアント。
//
// ゼロ値は無効である。newClient を通すこと。
type client struct {
	baseURL string
	http    *http.Client
}

// newClient は宛先とタイムアウトからクライアントを作る。
func newClient(o options) client {
	return client{
		baseURL: trimSlash(o.url),
		http: &http.Client{
			Transport:     nil, // 既定の Transport を使う
			CheckRedirect: nil, // 既定のリダイレクト方針
			Jar:           nil, // Cookie を持たない
			Timeout:       o.timeout,
		},
	}
}

// request は1回の HTTP 要求。
type request struct {
	method string
	// path は "/v1/search" のような絶対パス。クエリ文字列を含んでよい。
	path string
	// body は JSON 本文。nil なら本文を送らない。
	body []byte
	// tolerateStatus は「エラーではない 4xx/5xx」を1つだけ許す。
	//
	// 🔴 /healthz は依存が落ちていると 503 で Health を返す。これを Error
	// スキーマとして読むと「どの依存が落ちているか」が失われ、CLI は
	// 「サーバが 503 を返した」としか言えなくなる。0 は何も許さないことを表す。
	tolerateStatus int
}

// serverError はサーバが 4xx/5xx を返したこと。終了コード 2 になる。
type serverError struct {
	status  int
	code    string
	message string
}

// Error は error インタフェースを満たす。
func (e serverError) Error() string {
	return fmt.Sprintf("server returned %d: %s: %s", e.status, e.code, e.message)
}

// apiErrorDetail は OpenAPI の Error スキーマの中身。
type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiErrorBody は OpenAPI の Error スキーマ。
type apiErrorBody struct {
	Error apiErrorDetail `json:"error"`
}

// do は要求を送り、応答本文をそのまま返す。
//
// 復号せずにバイト列を返すのは、--json が「サーバ応答の生 JSON を整形せずに」
// 出す契約だからである。ここで型に落とすと、その生の形が失われる。
//
// 失敗の区別がそのまま終了コードになるので、3種を別の error で返す:
// 接続できない (errConnect / 3)・サーバが 4xx/5xx (serverError / 2)・
// それ以外 (1)。
func (c client) do(ctx context.Context, req request) ([]byte, error) {
	httpReq, err := c.build(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 接続不能・タイムアウト・ctx キャンセルがすべてここに来る。
		return nil, fmt.Errorf("%w: %s: %w", errConnect, c.baseURL, err)
	}

	// 本文を読み切ってから閉じる。読み取りと後始末の失敗をまとめて1つの error に
	// する（errors.Join は nil を落とすので、どちらも成功したときだけ nil になる）。
	raw, readErr := io.ReadAll(resp.Body)
	if err := errors.Join(readErr, resp.Body.Close()); err != nil {
		return nil, fmt.Errorf("%w: read response: %w", errConnect, err)
	}

	if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode != req.tolerateStatus {
		return nil, decodeServerError(resp.StatusCode, raw)
	}

	return raw, nil
}

// build は *http.Request を組み立てる。
func (c client) build(ctx context.Context, req request) (*http.Request, error) {
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, c.baseURL+req.path, body)
	if err != nil {
		// URL が壊れている場合がここに来る。利用者が直せる誤りなので終了コード 1。
		return nil, fmt.Errorf("%w: %w", errUsage, err)
	}

	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	return httpReq, nil
}

// decodeServerError はエラー応答を serverError にする。
//
// Error スキーマとして読めない応答（リバースプロキシの HTML 等）でも
// 状態コードは分かるので、本文をそのまま message に入れて捨てない。
func decodeServerError(status int, raw []byte) error {
	var body apiErrorBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Error.Code == "" {
		return serverError{
			status:  status,
			code:    "unparsable_error",
			message: strings.TrimSpace(string(raw)),
		}
	}

	return serverError{status: status, code: body.Error.Code, message: body.Error.Message}
}
