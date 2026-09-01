package httpapi

import (
	"log/slog"
	"net/http"
)

// healthCheck は依存1件の状態。
//
// 🔴 Detail に error の文字列を載せない。/healthz は認証を要求しない口であり、
// Postgres の接続失敗メッセージには DSN（ユーザ名・データベース名・ホスト）が、
// 埋め込みの失敗には接続先 URL が含まれる。どの依存が落ちているかは Name で
// 分かるので、診断に必要な情報は失われない。詳細は slog にだけ残す。
type healthCheck struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// healthResponse は GET /healthz の応答。
//
// map[string]any ではなく型で書くのは、応答の形が OpenAPI と一致していることを
// コンパイラに見張らせるため (GO-006)。
type healthResponse struct {
	Status     string                 `json:"status"`
	Checks     map[string]healthCheck `json:"checks"`
	EmbedderID string                 `json:"embedder_id"`
}

// 依存の状態。OpenAPI の Health.status の enum と対応する。
const (
	// statusOK は全ての依存が応答している。
	statusOK = "ok"
	// statusDegraded は一部が落ちているが、動く操作が残っている。
	statusDegraded = "degraded"
	// statusDown は中核（DB）が落ちていて何もできない。
	statusDown = "down"
)

// evaluateHealth は全 probe を実行して全体の状態を決める。
//
// 🔴 全体の状態は「最も重い故障」で決まる。DB が落ちていれば down、
// それ以外だけが落ちていれば degraded。degraded も 503 を返すが、状態を
// 区別するのは運用者が復旧の順序を決められるようにするためである
// （DB が生きているなら削除は動くので、取り込みだけ止めればよい）。
func (s *Server) evaluateHealth(r *http.Request) (healthResponse, int) {
	checks := make(map[string]healthCheck, len(s.deps.Probes))
	status := statusOK

	for _, probe := range s.deps.Probes {
		err := probe.Check(r.Context())
		if err == nil {
			checks[probe.Name] = healthCheck{Status: statusOK, Detail: ""}

			continue
		}

		// 詳細はログにだけ出す。応答には載せない（healthCheck の doc を参照）。
		s.log.Warn("health probe failed",
			slog.String("probe", probe.Name),
			slog.Any("error", err),
		)

		checks[probe.Name] = healthCheck{Status: statusDown, Detail: ""}

		if probe.Name == databaseProbeName {
			status = statusDown
		} else if status == statusOK {
			status = statusDegraded
		}
	}

	httpStatus := http.StatusOK
	if status != statusOK {
		httpStatus = http.StatusServiceUnavailable
	}

	return healthResponse{
		Status:     status,
		Checks:     checks,
		EmbedderID: s.deps.EmbedderID,
	}, httpStatus
}

// handleHealthz は依存の状態を返す。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	body, status := s.evaluateHealth(r)
	s.writeJSON(w, status, body)
}

// handleReadyz は受け入れ可否だけを返す。
//
// 判定は /healthz と同じにする。二つの口が別々の基準で答えると、
// 「readyz は通るのに検索が 503」という説明のつかない状態が生まれる。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	_, status := s.evaluateHealth(r)
	w.WriteHeader(status)
}
