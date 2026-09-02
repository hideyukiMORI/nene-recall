package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerScheme は Authorization ヘッダの認証方式。
//
// RFC 7235 は方式名を大文字小文字を区別せずに比較すると定めている。
// 比較を EqualFold で行うのはそのためで、"bearer" を弾かない。
const bearerScheme = "bearer"

// requireToken は共有 Bearer トークンを確かめる middleware を返す。
//
// 🔴 設定が空なら next をそのまま返す。「未設定なら認証なし」は
// docs/adr/0020-phase2-corpus-integration-contract.md の Decision 3 が定めた
// 既定であり、個人のローカル利用でトークンの用意を強制しないためである。
// ⚠️ ここに「未設定なら既定のトークン」を書かないこと。既定値のある共有秘密は、
// 設定を忘れた全員が同じ鍵を使う状態になる。
//
// 🔴 /healthz と /readyz には掛けない（Routes が掛ける範囲を決めている）。
// 監視の口が認証を要求すると、トークンを持たない監視系から
// 「落ちている」と「認証されていない」の区別がつかなくなる。
// 健全性の応答に秘匿情報を載せていないことが、この判断の前提である
// (healthCheck の doc を参照)。
func (s *Server) requireToken(next http.Handler) http.Handler {
	if s.cfg.APIToken == "" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenMatches(r.Header.Get("Authorization"), s.cfg.APIToken) {
			// RFC 7235 は 401 に WWW-Authenticate を要求する。
			// これが無いと、クライアントは「何を出せばよいか」を知る手立てが無い。
			w.Header().Set("WWW-Authenticate", "Bearer")

			// 🔴 応答にトークンを出さない。期待値も、受け取った値も、その長さも。
			// 失敗の理由を細かく返すほど、総当たりの手掛かりが増える。
			s.writeError(w, http.StatusUnauthorized, "unauthorized",
				"a valid bearer token is required")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// tokenMatches は Authorization ヘッダが期待するトークンと一致するかを返す。
//
// 🔴 比較は crypto/subtle の定数時間比較で行う。== で比べると、最初に違う
// バイトの位置で早期に返るため、応答時間から1バイトずつ正解を絞り込める。
// 総当たりが現実的でない長さのトークンでも、この経路は現実的になりうる。
//
// ⚠️ 長さの違いは ConstantTimeCompare が 0 を返す形で漏れる。これは避けられず、
// 秘密の長さを隠すことは目的にしていない（隠すのは中身である）。
//
// 期待値が空のときにここへ来ることは無い（requireToken が middleware を
// 掛けないため）が、来ても false になる——空トークンで素通りする経路を
// 関数単体でも作らない。
func tokenMatches(header, expected string) bool {
	scheme, presented, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) {
		return false
	}

	if expected == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}
