// Command recallctl は NeNe Recall の HTTP API を叩く薄いクライアントである。
//
// 🔴 ストア (PostgreSQL) にも埋め込み (Ollama) にも直接触らない。CLI が知っているのは
// サーバの URL だけである。判断の正本は
// docs/adr/0016-cli-is-an-http-client-with-org-default.md の Decision 1:
// org_id の必須化・入力検証・埋め込みモデルの整合チェックは HTTP 層に1箇所で
// 実装されており、CLI が DB を直接読むとその検査を二重に持つか素通しするかの
// どちらかになる。素通しは docs/adr/0003-org-id-is-mandatory.md の抜け穴になり、
// 二重化は片方だけ直される。
//
// 🔴 org_id の既定値を持つのはこのパッケージだけである (ADR 0016 Decision 2)。
// サーバ側には決して置かない。定数は options.go の defaultOrgID にある。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// 終了コード。docs/adr/0016-cli-is-an-http-client-with-org-default.md Decision 3。
//
// 🔴 「使い方の誤り」と「サーバが拒否した」と「サーバに届かなかった」を分けるのは、
// スクリプトから呼んだときに再試行してよいのがどれかを区別するためである。
// 3 (接続失敗) だけが「後でもう一度やれば通るかもしれない」失敗である。
const (
	exitOK      = 0
	exitUsage   = 1
	exitServer  = 2
	exitConnect = 3
)

// errUsage は使い方・入力の誤り。終了コード 1 になる。
var errUsage = errors.New("recallctl: invalid usage")

// errConnect はサーバへ到達できなかったこと。終了コード 3 になる。
var errConnect = errors.New("recallctl: cannot reach the server")

// errUnhealthy は /healthz が ok 以外を返したこと。終了コード 2 になる。
var errUnhealthy = errors.New("recallctl: the server is not healthy")

// usage は recallctl 全体の使い方。
//
// 各サブコマンドの詳細はそのサブコマンドの -h が出す（flag が自動で組み立てる）。
// ここに全フラグを書き写すと、フラグを足したときに必ず片方だけが古くなる。
const usage = `recallctl — NeNe Recall の HTTP API を叩くクライアント

使い方:
  recallctl <command> [flags] [args]

コマンド:
  search <query...>          チャンクを検索する (POST /v1/search)
  put [file]                 チャンクを投入する (POST /v1/chunks)。JSONL・省略時は標準入力
  delete <chunk_id>            チャンクを1件削除する (DELETE /v1/chunks/{chunk_id})
  delete-source <source_id>    source 単位で一括削除する (DELETE /v1/sources/{source_id}/chunks)
  delete-document <doc_id>     document 単位で一括削除する (DELETE /v1/documents/{document_id}/chunks)
  health                       依存の状態を見る (GET /healthz)

共通フラグ (各コマンドの -h も参照):
  -url      サーバの URL。既定は環境変数 RECALL_URL、無ければ http://127.0.0.1:8080
  -org      org_id。既定は環境変数 RECALL_ORG_ID、無ければ 1
  -token    Bearer トークン。既定は環境変数 RECALL_TOKEN、無ければ付けない
  -timeout  1リクエストの上限。既定 60s
  -json     サーバ応答の生 JSON をそのまま標準出力へ出す

終了コード:
  0 成功 / 1 使い方・入力の誤り / 2 サーバが 4xx/5xx / 3 接続失敗

🔴 フラグは位置引数より前に置くこと。flag は最初の非フラグ引数で解釈を止めるので、
   recallctl delete 42 -org 5 の -org 5 はフラグとして読まれない。
`

// streams はプロセスの入出力。
//
// os に触るのを main の1行に閉じることで、残り全部をテストから駆動できる
// (ARC-005 の配線点)。標準出力には結果だけを、診断は標準エラーへ出す。
type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

func main() {
	os.Exit(run(os.Args[1:], streams{in: os.Stdin, out: os.Stdout, err: os.Stderr}))
}

// run は引数を解釈してサブコマンドを走らせ、終了コードを返す。
//
// os.Exit を呼ばないのは、この関数をテストから駆動できるようにするためである。
func run(args []string, s streams) int {
	if len(args) == 0 {
		newTextWriter(s.err).write(usage)

		return exitUsage
	}

	if isHelp(args[0]) {
		newTextWriter(s.out).write(usage)

		return exitOK
	}

	// Ctrl-C で埋め込み待ちの検索を止められるようにする。ctx は HTTP 要求まで届く。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := dispatch(ctx, args[0], args[1:], s)
	if err == nil {
		return exitOK
	}

	// -h は誤りではない。flag が既に使い方を出しているので、二重に報告しない。
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}

	report(s.err, err)

	return exitCode(err)
}

// isHelp は最初の引数が使い方の要求かを判定する。
func isHelp(name string) bool {
	return name == "-h" || name == "-help" || name == "--help" || name == "help"
}

// dispatch はサブコマンドを選ぶ。
func dispatch(ctx context.Context, name string, args []string, s streams) error {
	switch name {
	case "search":
		return cmdSearch(ctx, args, s)
	case "put":
		return cmdPut(ctx, args, s)
	case "delete":
		return cmdDelete(ctx, args, s)
	case "delete-source":
		return cmdDeleteSource(ctx, args, s)
	case "delete-document":
		return cmdDeleteDocument(ctx, args, s)
	case "health":
		return cmdHealth(ctx, args, s)
	}

	return fmt.Errorf("%w: unknown command %q (recallctl help でコマンド一覧が出る)", errUsage, name)
}

// report は失敗を標準エラーへ1行で書く。
//
// 🔴 サーバの拒否は error.code と error.message をそのまま見せる。code は
// OpenAPI の契約なので、それを見れば利用者は次の一手を選べる。
func report(w io.Writer, err error) {
	out := newTextWriter(w)

	var srvErr serverError
	if errors.As(err, &srvErr) {
		out.printf("error: http %d: %s: %s\n", srvErr.status, srvErr.code, srvErr.message)

		return
	}

	out.printf("error: %s\n", err.Error())
}

// exitCode は error を終了コードに写す。
//
// 🔴 判定は errors.As / errors.Is だけで行い、メッセージ文字列で分岐しない
// (GO-005)。表に無い失敗は「使い方の誤り」に落とす——入力を読めない・応答を
// 解釈できないは、いずれも呼び出し側が直せる種類の失敗である。
func exitCode(err error) int {
	var srvErr serverError

	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &srvErr):
		return exitServer
	case errors.Is(err, errUnhealthy):
		return exitServer
	case errors.Is(err, errConnect):
		return exitConnect
	default:
		return exitUsage
	}
}

// newFlagSet はサブコマンド用の FlagSet を作る。
//
// 出力先を注入するのは、テストがプロセスの標準エラーを覗かずに済むようにするため。
func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("recallctl "+name, flag.ContinueOnError)
	fs.SetOutput(out)

	return fs
}
