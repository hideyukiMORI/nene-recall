package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// 環境変数名。プロセス環境を読んでよいのは配線点 (cmd) だけである (ARC-005)。
const (
	envURL   = "RECALL_URL"
	envOrgID = "RECALL_ORG_ID"
	// envToken はサーバの RECALL_API_TOKEN と対になる共有トークン。
	//
	// 🔴 変数名をサーバ側と別にしてある。同じ機械で両方を動かすとき、サーバの
	// 環境変数がクライアントにも効くと「設定した覚えのないトークンが付いていた」
	// が起きる。送る側と受ける側は別々に設定させる。
	envToken = "RECALL_TOKEN"
)

// defaultURL はサーバの既定の宛先。
//
// localhost ではなく 127.0.0.1 を書くのは、WSL では localhost が IPv6 (::1) に
// 解決されることがあり、IPv4 だけで待ち受けているサーバに届かないためである。
const defaultURL = "http://127.0.0.1:8080"

// defaultTimeout は1リクエストの上限。
//
// 検索は埋め込み API の往復を含む。コールドスタート（モデルのロードを含む初回）は
// 実測 18.4 秒 (docs/benchmarks/2026-09-01-baseline.md) なので、初回のロードを
// 跨いでも間に合い、かつ無期限にはならない値を置く。
const defaultTimeout = 60 * time.Second

// defaultOrgID は org_id の最後の拠り所である。
//
// 🔴 このリポジトリで「既定の org」が書かれているのはこの1行だけである
// (docs/adr/0016-cli-is-an-http-client-with-org-default.md Decision 2)。
// サーバ側には決して置かない——検索の分離条件は Go 側の責任に移っており、
// 緩めるとあるテナントの検索が別テナントの文書を返す
// (docs/adr/0003-org-id-is-mandatory.md)。しかも単一テナントで使っている限り
// この欠陥は一切症状を出さない。
//
// 値は必ず org.NewID を通す。org.ID(1) の直接変換は CNF-001 が禁じており、
// 既定値であってもその抜け道を作らない。
const defaultOrgID = 1

// orgSource は org_id をどこから取ったかを表す。
//
// 表示のためだけに持つ。🔴 既定値が黙って効いていると、別 org のデータを見て
// いるつもりで自分のデータを見ている、という取り違えが起きる (ADR 0016 Decision 2)。
type orgSource string

const (
	orgFromFlag    orgSource = "flag"
	orgFromEnv     orgSource = "env"
	orgFromDefault orgSource = "default"
)

// commonFlags は全サブコマンド共通のフラグの受け皿。
//
// org を string で受けるのは、「指定されなかった」と「0 を指定した」を
// 区別するためである。int で受けると未指定が 0 になり、環境変数と既定値へ
// 落ちる経路と 0 の拒否が同じ枝になる。
type commonFlags struct {
	url     string
	org     string
	token   string
	timeout time.Duration
	asJSON  bool
}

// registerCommon は共通フラグを FlagSet に登録する。
func registerCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{url: "", org: "", token: "", timeout: defaultTimeout, asJSON: false}

	fs.StringVar(&c.url, "url", "",
		"サーバの URL（未指定なら $"+envURL+"、無ければ "+defaultURL+"）")
	fs.StringVar(&c.org, "org", "",
		"org_id（未指定なら $"+envOrgID+"、無ければ既定値）")
	// 🔴 既定値を持たせない。トークンは秘密であり、「未指定なら既定のトークン」に
	// すると設定を忘れた全員が同じ鍵を使う状態になる。省略時はヘッダを付けない。
	fs.StringVar(&c.token, "token", "",
		"Bearer トークン（未指定なら $"+envToken+"、無ければ付けない）")
	fs.DurationVar(&c.timeout, "timeout", defaultTimeout, "1リクエストの上限")
	fs.BoolVar(&c.asJSON, "json", false, "サーバ応答の生 JSON をそのまま標準出力へ出す")

	return c
}

// options は解決済みの共通設定。
type options struct {
	url   string
	orgID org.ID
	// token は Bearer トークン。空ならヘッダを付けない。
	//
	// 🔴 これを診断行に出さないこと。announceOrg が出すのは org_id だけである。
	// どこから取ったか（フラグか環境変数か）も出さない——org_id と違って、
	// 取り違えても他人のデータが見えるわけではなく、出す利益が無い。
	token   string
	orgFrom orgSource
	timeout time.Duration
	asJSON  bool
}

// resolve はフラグ・環境変数・既定値から共通設定を決める。
func (c *commonFlags) resolve() (options, error) {
	id, from, err := resolveOrgID(c.org)
	if err != nil {
		return options{}, err
	}

	return options{
		url:     resolveURL(c.url),
		orgID:   id,
		token:   resolveToken(c.token),
		orgFrom: from,
		timeout: c.timeout,
		asJSON:  c.asJSON,
	}, nil
}

// resolveToken は --token → $RECALL_TOKEN の順でトークンを決める。
//
// 🔴 org_id と違って既定値は無い。どちらも空なら空を返し、client が
// Authorization ヘッダを付けない。サーバ側が認証を要求していなければそれで通り、
// 要求していれば 401 が返る——「付けたつもりで付いていない」は 401 で必ず表面化する。
func resolveToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	return os.Getenv(envToken)
}

// resolveURL は --url → $RECALL_URL → 既定 の順で宛先を決める。
func resolveURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	if fromEnv := os.Getenv(envURL); fromEnv != "" {
		return fromEnv
	}

	return defaultURL
}

// resolveOrgID は --org → $RECALL_ORG_ID → 定数 defaultOrgID の順で org_id を決める。
//
// どこから取ったかを一緒に返すのは、呼び出し側が必ず表示するためである。
// 🔴 「空なら 1」を org.ParseID の中に書かないこと。それは ADR 0003 が禁じている
// フォールバックそのもので、サーバ側にも同じ形で書けてしまう。
func resolveOrgID(flagValue string) (org.ID, orgSource, error) {
	if flagValue != "" {
		id, err := org.ParseID(flagValue)
		if err != nil {
			return 0, orgFromFlag, fmt.Errorf("%w: -org: %w", errUsage, err)
		}

		return id, orgFromFlag, nil
	}

	if fromEnv := os.Getenv(envOrgID); fromEnv != "" {
		id, err := org.ParseID(fromEnv)
		if err != nil {
			return 0, orgFromEnv, fmt.Errorf("%w: $%s: %w", errUsage, envOrgID, err)
		}

		return id, orgFromEnv, nil
	}

	id, err := org.NewID(defaultOrgID)
	if err != nil {
		return 0, orgFromDefault, fmt.Errorf("%w: default org_id: %w", errUsage, err)
	}

	return id, orgFromDefault, nil
}

// announceOrg はどの org_id で問い合わせたかを標準エラーへ1行出す。
//
// 標準出力ではなく標準エラーへ出すのは、`recallctl search ... > out.json` の
// 出力を汚さないためである。
func announceOrg(w io.Writer, o options) {
	newTextWriter(w).printf("org_id=%s (%s)\n", o.orgID.String(), string(o.orgFrom))
}

// trimSlash は URL 末尾の / を落とす。パスと連結したときに // にしないため。
func trimSlash(rawURL string) string { return strings.TrimSuffix(rawURL, "/") }
