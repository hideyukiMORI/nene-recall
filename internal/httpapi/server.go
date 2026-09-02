// Package httpapi は HTTP のルーティングと入出力の変換を担う。
//
// 具体ストアや具体 Embedder は import しない。依存は index.Searcher /
// index.Writer / Probe として注入される（ARC-001・depguard が強制する）。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// Probe は依存1件の生存確認。
//
// 名前を持つのは、/healthz がどの依存が落ちているかを返すためである。
// 「どこかが落ちている」だけでは運用者は次の一手を選べない。
type Probe struct {
	// Name は応答の checks に出る名前。"database" / "embedder"。
	Name string
	// Check は到達性を確かめる。nil を返せば正常。
	Check func(ctx context.Context) error
}

// databaseProbeName は /healthz で致命的として扱う依存の名前。
//
// 🔴 DB が落ちていれば削除も含めて何もできないので down（503）。
// 埋め込みだけが落ちている場合は削除は動くので degraded（同じく 503 だが、
// 状態としては区別する）。この違いは運用者が復旧の順序を決めるための情報である。
const databaseProbeName = "database"

// Dependencies は Server が使う依存の束。
//
// 🔴 exhaustruct の対象にしてある。フィールドを足したのに配線を直し忘れると、
// ゼロ値の依存を持ったサーバが「起動はする」状態になる。
type Dependencies struct {
	// Searcher は検索の実装。
	Searcher index.Searcher
	// Writer は投入・削除の実装。
	Writer index.Writer
	// EmbedderID は /healthz と検索応答に載せる識別子。
	EmbedderID string
	// Probes は /healthz と /readyz が確かめる依存。
	Probes []Probe
}

// Server は HTTP ハンドラの集合。
//
// ゼロ値は無効である。必ず New を通すこと。
type Server struct {
	cfg  config.Config
	log  *slog.Logger
	deps Dependencies
}

// New は Server を組み立てる。
//
// 依存の欠落は error で返す。panic を使わないのは GO-005 の要求であり、
// 配線の誤りは運用者が読める形で起動時に落とすべきものだからである。
func New(cfg config.Config, log *slog.Logger, deps Dependencies) (*Server, error) {
	switch {
	case log == nil:
		return nil, fmt.Errorf("%w: logger is required", errMissingDependency)
	case deps.Searcher == nil:
		return nil, fmt.Errorf("%w: Searcher is required", errMissingDependency)
	case deps.Writer == nil:
		return nil, fmt.Errorf("%w: Writer is required", errMissingDependency)
	case deps.EmbedderID == "":
		return nil, fmt.Errorf("%w: EmbedderID is required", errMissingDependency)
	}

	return &Server{cfg: cfg, log: log, deps: deps}, nil
}

// Routes は http.Handler を返す。
//
// 🔴 /v1/* を1つの ServeMux にまとめてから requireToken で包む。ハンドラごとに
// 包む形にすると、新しいエンドポイントを足した人が包み忘れられる——そして
// 忘れても何も落ちない。認証されない口が1つ増えるだけである。
// まとめて包めば、/v1/ の下に足したものは自動的に同じ扱いになる。
//
// /healthz と /readyz は外側の mux に直接置く。監視の口は認証を要求しない
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 3)。
func (s *Server) Routes() http.Handler {
	v1 := http.NewServeMux()
	v1.HandleFunc("POST /v1/search", s.handleSearch)
	v1.HandleFunc("POST /v1/chunks", s.handlePutChunks)
	v1.HandleFunc("DELETE /v1/chunks/{chunk_id}", s.handleDeleteChunk)
	v1.HandleFunc("DELETE /v1/sources/{source_id}/chunks", s.handleDeleteBySource)
	v1.HandleFunc("DELETE /v1/documents/{document_id}/chunks", s.handleDeleteByDocument)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.Handle("/v1/", s.requireToken(v1))

	return mux
}

// apiError は Error スキーマの中身。docs/openapi/openapi.yaml と対応する。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorResponse は全エラー応答の外枠。
//
// 🔴 ここに設定値や内部状態を足さないこと。config.Config を丸ごと入れると
// VoyageAPIKey が応答に出る。エラー応答に載せてよいのは code と message だけ。
type errorResponse struct {
	Error apiError `json:"error"`
}

// writeJSON は JSON 応答を書く。
//
// body が any なのは JSON 符号化の境界だからで、この例外は encoder ヘルパに閉じる
// (GO-006 の適用外はここだけ)。符号化に失敗してもヘッダは送信済みなので、
// 復帰はできない。握り潰さずに記録だけ残す。
func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("failed to encode response body",
			slog.Int("status", status),
			slog.Any("error", err),
		)
	}
}

// writeError はエラー応答を書く。
func (s *Server) writeError(w http.ResponseWriter, status int, code, msg string) {
	s.writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: msg}})
}

// decodeJSON は要求本文を読む。読めなければ 400 を書いて false を返す。
//
// dst が any なのは writeJSON と同じ JSON 境界の例外である。
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")

		return false
	}

	return true
}

// writeRequestError は境界検証の失敗を 400 で返す。
//
// org.ErrInvalid だけは code を org_id_required に固定する。org_id の欠落は
// 最も起きやすい誤りで、専用の code があるほうが呼び出し側が対処しやすい。
func (s *Server) writeRequestError(w http.ResponseWriter, err error) {
	var ve validationError
	if errors.As(err, &ve) {
		s.writeError(w, http.StatusBadRequest, ve.code, ve.message)

		return
	}

	if errors.Is(err, org.ErrInvalid) {
		s.writeError(w, http.StatusBadRequest, "org_id_required",
			"org_id is required and must be a positive integer")

		return
	}

	s.writeError(w, http.StatusBadRequest, "invalid_request", "request is not valid")
}

// writeDependencyError は下位層の失敗を写像表に従って応答に写す。
//
// 🔴 表に無い error は 500 の固定文言にする。内部の error 文字列を応答に載せない。
// DSN・SQL・接続先が混じるためで、詳細は slog にだけ残す。
func (s *Server) writeDependencyError(w http.ResponseWriter, op string, err error) {
	for _, m := range failureMappings() {
		if !errors.Is(err, m.sentinel) {
			continue
		}

		s.log.Warn("dependency failure",
			slog.String("op", op),
			slog.String("code", m.code),
			slog.Any("error", err),
		)
		s.writeError(w, m.status, m.code, m.message)

		return
	}

	s.log.Error("unhandled failure",
		slog.String("op", op),
		slog.Any("error", err),
	)
	s.writeError(w, http.StatusInternalServerError, "internal", "internal server error")
}

// requireOrgID はクエリ文字列から org.ID を取り出す。
//
// 生成経路を org.ParseID の1本に絞っているので、「空なら既定値」という分岐は
// このパッケージからは書けない (ADR 0003)。
func (s *Server) requireOrgID(w http.ResponseWriter, r *http.Request) (org.ID, bool) {
	id, err := org.ParseID(r.URL.Query().Get("org_id"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "org_id_invalid", err.Error())

		return 0, false
	}

	return id, true
}

// requireBodyOrgID は本文の org_id を検証済みの org.ID に変換する。
//
// ポインタで受けるのは「0 が来た」と「キーが無かった」を区別するため。
// 値型だとどちらも 0 になり、欠落を検知できない。
func requireBodyOrgID(raw *int64) (org.ID, error) {
	if raw == nil {
		return 0, fmt.Errorf("%w: org_id is required", org.ErrInvalid)
	}

	id, err := org.NewID(*raw)
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}

	return id, nil
}
