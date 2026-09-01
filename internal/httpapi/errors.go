package httpapi

import (
	"errors"
	"net/http"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// errMissingDependency は Server の組み立てに必要な依存が欠けていることを表す。
//
// 🔴 panic ではなく error にする（GO-005）。配線の誤りは起動時に読める形で
// 落とすべきもので、スタックトレースを運用者に読ませる理由が無い。
var errMissingDependency = errors.New("httpapi: missing dependency")

// validationError は境界での入力検証の失敗。
//
// code はそのまま応答の error.code になる。呼び出し側が分岐するのは code であって
// メッセージではないので、両者を分けて持つ。
type validationError struct {
	code    string
	message string
}

// Error は error インタフェースを満たす。
func (e validationError) Error() string { return e.code + ": " + e.message }

// invalid は検証エラーを作る。
func invalid(code, message string) error {
	return validationError{code: code, message: message}
}

// failureMapping は下位層の sentinel と HTTP 応答の対応。
type failureMapping struct {
	sentinel error
	status   int
	code     string
	message  string
}

// failureMappings は error から HTTP 応答への写像表。
//
// 🔴 この表がこのパッケージの要である。判定はすべて errors.Is で行い、
// メッセージ文字列で分岐しない。表に無い error は 500 の固定文言に落ちる。
//
// | sentinel                      | status | code                  |
// | ----------------------------- | ------ | --------------------- |
// | index.ErrInvalidQuery         | 400    | invalid_query         |
// | index.ErrEmbedderMismatch     | 503    | embedder_mismatch     |
// | embed.ErrProviderUnavailable  | 503    | embedder_unavailable  |
// | それ以外                        | 500    | internal              |
//
// 関数にしているのはパッケージ変数を持たないため（GO-007）。
func failureMappings() []failureMapping {
	return []failureMapping{
		{
			sentinel: index.ErrInvalidQuery,
			status:   http.StatusBadRequest,
			code:     "invalid_query",
			message:  "the search query is not valid",
		},
		{
			// 保存済みベクトルと現在のモデルが違う。取り込み直しが要る状態で、
			// 🔴 「検索結果が空」で隠さない（ADR 0005）。
			sentinel: index.ErrEmbedderMismatch,
			status:   http.StatusServiceUnavailable,
			code:     "embedder_mismatch",
			message:  "stored vectors were produced by a different embedding model",
		},
		{
			// Ollama が落ちている・モデル未取得・応答が壊れている。
			sentinel: embed.ErrProviderUnavailable,
			status:   http.StatusServiceUnavailable,
			code:     "embedder_unavailable",
			message:  "the embedding provider is unavailable",
		},
	}
}
