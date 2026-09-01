package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// embedRequest は POST /api/embed の本文。
//
// エンドポイントは /api/embed である。旧 /api/embeddings は使わない。
// 正規化済み・1024次元を実測したのは /api/embed のほうで、旧エンドポイントは
// 応答の形も異なる (docs/benchmarks/2026-09-01-baseline.md §7)。
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse は POST /api/embed の応答。
//
// Error は Ollama が失敗を JSON で返すときに埋まる（モデル未取得など）。
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

// showRequest は POST /api/show の本文。
type showRequest struct {
	Model string `json:"model"`
}

// errorResponse は失敗時に Ollama が返す JSON。
type errorResponse struct {
	Error string `json:"error"`
}

// marshalJSON は要求本文を JSON にする。
func marshalJSON(payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		// 自前の構造体しか渡さないので実際には起きないが、握り潰さない。
		return nil, fmt.Errorf("%w: encode request: %w", embed.ErrProviderUnavailable, err)
	}

	return body, nil
}

// post は JSON を POST して応答本文と状態コードを返す。
//
// 🔴 リトライしない。失敗のほとんどは「Ollama が起動していない」「アドレスが
// 変わった」という設定・運用の誤りで、リトライは症状を遅らせるだけである。
// 加えて待ち時間の実装には time.Sleep が要り、これは forbidigo が禁じている
// (GO-013)。即座に失敗させ、/readyz の 503 と検索時の 503 で見せる。
//
// タイムアウトは注入された *http.Client と ctx の二重で掛かる。
func (c *Client) post(ctx context.Context, path string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: build request: %w", embed.ErrProviderUnavailable, err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// 接続不能・タイムアウト・ctx キャンセルがすべてここに来る。
		// 元の error を連鎖に残すので、呼び出し側は context.Canceled も判別できる。
		return nil, 0, fmt.Errorf("%w: %s: %w", embed.ErrProviderUnavailable, c.baseURL, err)
	}

	// 本文を読み切ってから閉じる。読み取りと後始末の失敗をまとめて1つの error にする
	// （errors.Join は nil を落とすので、どちらも成功したときだけ nil になる）。
	body, readErr := io.ReadAll(resp.Body)
	if err := errors.Join(readErr, resp.Body.Close()); err != nil {
		return nil, 0, fmt.Errorf("%w: read response: %w", embed.ErrProviderUnavailable, err)
	}

	return body, resp.StatusCode, nil
}

// classifyStatus は非 200 応答を運用者が読める error にする。
//
// 🔴 404 は「モデルが取得されていない」ことがほとんどなので、次の一手
// (ollama pull <model>) をメッセージに含める。エラーを読んだ人がその場で
// 直せるかどうかは、メッセージに何が書いてあるかで決まる。
func (c *Client) classifyStatus(status int, body []byte) error {
	detail := decodeErrorMessage(body)

	if status == http.StatusNotFound {
		return fmt.Errorf("%w: model %q is not available on %s (%s): run `ollama pull %s`",
			embed.ErrProviderUnavailable, c.model, c.baseURL, detail, c.model)
	}

	return fmt.Errorf("%w: %s returned HTTP %d (%s)",
		embed.ErrProviderUnavailable, c.baseURL, status, detail)
}

// decodeErrorMessage は Ollama の error フィールドを取り出す。
//
// JSON でない応答（プロキシの HTML など）も来うるので、失敗しても落とさない。
func decodeErrorMessage(body []byte) string {
	var parsed errorResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != "" {
		return parsed.Error
	}

	const maxDetail = 200
	if len(body) > maxDetail {
		return string(body[:maxDetail])
	}

	if len(body) == 0 {
		return "empty response body"
	}

	return string(body)
}

// embedBatch は1リクエストぶんを埋め込む。
func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	payload, err := marshalJSON(embedRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, err
	}

	body, status, err := c.post(ctx, "/api/embed", payload)
	if err != nil {
		return nil, err
	}

	if status != http.StatusOK {
		return nil, c.classifyStatus(status, body)
	}

	var parsed embedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode response: %w", embed.ErrProviderUnavailable, err)
	}

	// 200 でも error フィールドが埋まっていることがある。黙って空を返さない。
	if parsed.Error != "" {
		return nil, fmt.Errorf("%w: %s", embed.ErrProviderUnavailable, parsed.Error)
	}

	return c.prepareVectors(parsed.Embeddings, len(texts))
}
