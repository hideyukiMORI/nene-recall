// Command eval は検索品質を計測する。
//
// ADR 0009 が要求する recall@k・MRR・p95（埋め込み往復を含む／除く の両方）を
// 測り、機械可読な JSON レポートを書き出す。設計判断の記録は
// docs/adr/0013-evaluation-harness-design.md にある。
//
// 🔴 ここは配線点である。具体ストア (internal/store/postgres) と
// 具体 Embedder (internal/embed/ollama) を import してよいのは cmd だけで、
// depguard がそれを強制する (ARC-001)。計測のロジックは internal/eval にあり、
// このファイルは組み立てと入出力だけを持つ。
//
// 🔴 これは「検査」ではなく「計測」なので make check には入っていない。
// recall@10 = 0.83 は真でも偽でもなく、数十クエリの指標に自動 fail の閾値を
// 切ると1クエリ分のゆらぎで CI が赤くなる。線引きの理由は ADR 0013 の
// Decision 5 と Makefile の eval ターゲットのコメントを参照。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	// 評価用 DB の作り直しにも pgx を使う。blank import なのは、
	// 必要なのが database/sql への登録だけだからである。
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
	"github.com/hideyukiMORI/nene-recall/internal/eval"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/union"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// 🔴 接続情報は compose.yaml・.github/workflows/ci.yml・.env.example・
// internal/store/postgres/main_test.go と同一の固定値である。
// **5箇所が同じでなければならない**（この cmd が5箇所目・ADR 0013）。
//
// 🔴 ポートが 5433 なのは 5432 をネイティブ PostgreSQL が占有しているため。
// 標準ポートに戻すとネイティブ側へ繋がり、「コンテナは healthy なのに
// SASL 認証失敗」という辿りにくい壊れ方をする。詳細は compose.yaml のコメント。
//
// 環境変数から読まないのは main_test.go と同じ理由である。ローカルと CI が
// 同じ DSN で同じものを見ることを、env で分岐できる余地ごと無くしておく。
//
// 🔴 可変なのは**DB 名だけ**である（-eval-db）。ホスト・ポート・認証情報は
// 定数のままで、別の Postgres を向ける口は開けない。DB 名を可変にしたのは、
// 複数のレーンが同じ Postgres に対して同時に計測するとき、共有の recall_eval を
// 互いに DROP して壊し合うためである。
const (
	dsnHead = "postgres://recall:recall@localhost:5433/"
	dsnTail = "?sslmode=disable"

	adminDSN = dsnHead + "recall" + dsnTail

	// defaultEvalDBName は -eval-db の既定値であり、許される名前の接頭辞でもある。
	defaultEvalDBName = "recall_eval"
)

// evalDSN は評価用 DB への接続文字列を組み立てる。
//
// 🔴 名前は validateEvalDBName を通ったものだけを渡すこと。DB 名は識別子なので
// プレースホルダにできず、ここは文字列連結になる。
func evalDSN(dbName string) string {
	return dsnHead + dbName + dsnTail
}

// defaultSQLitePath は -store sqlite のときの既定のファイル。
//
// 🔴 bin/ に置くのは .gitignore 済みの生成物置き場だからである。評価用の
// ファイルは毎回作り直される中間生成物であって、リポジトリに残すものではない。
const defaultSQLitePath = "bin/recall_eval.db"

// evalTimeout は計測全体の上限。
//
// コーパスの投入（埋め込みを含む）とクエリ数 ×（1 + ラウンド数）回の検索を
// 収める必要がある。実測 87.8 件/秒 の埋め込みで 1,000 チャンクなら約 11 秒、
// 検索は 1回 50〜100ms 程度なので、数十クエリなら数分で終わる。
//
// 🔴 2 時間なのは -distractors で 10万件を足せるようになったからである
// (docs/adr/0019-large-scale-benchmark-corpus.md)。ADR 0019 自身が 10万件の
// 埋め込みを約18分と見積もっており、その上に 10万件ぶんの全探索が
// クエリ数 ×2系統 ×(1 + ラウンド数) 回だけ乗る。索引を入れる**前**に測るのが
// ADR 0007 の手順なので、この回数は速くならない。
//
// ⚠️ 30 分では足りない（初回の配線確認で実測）。上限は「無期限にはしない」
// ためのものであって目標値ではないが、**測りたいものが入らない上限は
// 上限として機能していない**——途中で切れた実行は、何も測れないまま
// 埋め込みの時間だけを消費する。
const evalTimeout = 2 * time.Hour

// ollamaTimeout は埋め込み1リクエストの上限。cmd/recall と同じ根拠（コールド
// スタート実測 18.4 秒を跨いでも間に合い、かつ無期限にはならない値）。
const ollamaTimeout = 60 * time.Second

// alphaFromConfig は -alpha が指定されなかったことを表す番兵。
//
// 負の値は Options.validate が拒否する範囲なので、「未指定」と混同されない。
const alphaFromConfig = -1

// reportFileMode / reportDirMode は書き出すレポートの権限。
const (
	reportFileMode = 0o644
	reportDirMode  = 0o755
)

var (
	// errFlags はフラグの指定が足りない・矛盾していることを表す。
	errFlags = errors.New("eval: invalid command line flags")
	// errDataset は評価セットのファイルを読めないことを表す。
	errDataset = errors.New("eval: cannot read the evaluation set")
	// errEvalDatabase は評価用 DB の作り直しに失敗したことを表す。
	errEvalDatabase = errors.New("eval: cannot prepare the evaluation database")
	// errEnvironment は環境の記録を集められないことを表す。
	errEnvironment = errors.New("eval: cannot collect the environment")
	// errReport はレポートを書き出せないことを表す。
	errReport = errors.New("eval: cannot write the report")
	// errEmbedderNotSupported は ollama 以外のプロバイダが指定されたことを表す。
	errEmbedderNotSupported = errors.New("eval: only the ollama embedder is supported")
	// errEmbedCount は埋め込みが1クエリに対して1本を返さなかったことを表す。
	errEmbedCount = errors.New("eval: the embedder did not return exactly one vector")
	// errUnknownStore は -store に未知の名前が渡されたことを表す。
	//
	// 🔴 未知の指定を既定へ黙って倒さない。綴り誤りが「postgres で測った」結果
	// として記録され、後から条件を取り違える。postgres.ParseFusion と同じ理由。
	errUnknownStore = errors.New("eval: unknown store")
	// errUnknownTokenizer は -tokenizer に未知の名前が渡されたことを表す。
	//
	// 🔴 errUnknownStore と同じ理由で既定へ倒さない。綴り誤りが「bigram で
	// 測った」結果として記録されると、ADR 0018 の比較そのものが無意味になる。
	errUnknownTokenizer = errors.New("eval: unknown tokenizer")
	// errEvalDBName は -eval-db に使えない名前が渡されたことを表す。
	//
	// 🔴 このコマンドは指定された DB を **DROP してから CREATE する**。名前を
	// 自由にすると「任意の DB を消せる口」になる。recall_eval で始まり
	// [a-z0-9_] だけが続く名前に限り、開発用の recall・テスト用の
	// recall_test_<pid>・無関係な DB には触れられないようにする。
	errEvalDBName = errors.New("eval: the evaluation database name is not allowed")
)

// storeNames は -store に指定できる名前。
//
// 🔴 config.Store の値と同じ綴りである。評価だけ別の名前を持つと、
// レポートの conditions.ranking.store と .env の RECALL_STORE が対応しない。
const (
	storeNamePostgres = string(config.StorePostgres)
	storeNameSQLite   = string(config.StoreSQLite)
)

// tokenizerNames は -tokenizer に指定できる名前。
//
// 🔴 config.Tokenizer の値と同じ綴りである。評価だけ別の名前を持つと、
// レポートと .env の RECALL_TOKENIZER が対応しない。
const (
	tokenizerNameBigram = string(config.TokenizerBigram)
	tokenizerNameKagome = string(config.TokenizerKagome)
	tokenizerNameUnion  = string(config.TokenizerUnion)
)

// tokenizerNameList は -tokenizer のヘルプとエラーに出す選択肢の一覧。
//
// 🔑 名前を1箇所から作る。選択肢を足したときにヘルプかエラーの片方だけ古く
// なると、「指定できない値が指定できるように読める」文言が残る。
const tokenizerNameList = tokenizerNameBigram + " | " + tokenizerNameKagome + " | " + tokenizerNameUnion

// alphaNotePostgres / alphaNoteSQLite は alpha の読み方をレポート自身に
// 書き残す文言。
//
// 🔴 レポートは単体で読まれる。数字だけを載せると、読んだ人はそれが普遍的に
// 調整済みの値だと受け取る (CLAUDE.md 地雷7)。
//
// 🔴 ストアごとに文言を分ける。ADR 0015 が 0.8 を選んだのは postgres
// (ts_rank・クエリ内正規化) の掃引であって、SQLite (FTS5 の bm25) は対象外で
// ある。同じ但し書きを両方に付けると、SQLite でも 0.8 が選ばれた形跡がある
// ように読める——実測のプラトーは 0.8〜0.9 で postgres とずれている。
// **測っていないことをレポートに書かない。**
//
// 🔴 文言が internal/eval ではなくここにあるのは、ストアを知ってよいのが
// 配線点だけだからである (ARC-001)。
const (
	alphaNotePostgres = "the default 0.8 was chosen on the 2026-09-02 eval set " +
		"(bge-m3:1024, bigram, per-query lexical normalization); it is the centre of the " +
		"0.7-0.9 plateau, not a universal optimum (ADR 0015). " +
		"Re-measure if any of those conditions change."
	alphaNoteSQLite = "ADR 0015 does not cover the sqlite store: the default 0.8 was " +
		"chosen on postgres (ts_rank), not here. The 2026-09-02 sweep on sqlite " +
		"(bge-m3:1024, bigram, fts5 bm25) put the plateau at 0.8-0.9, so 0.8 falls " +
		"inside it but was not selected for this backend " +
		"(docs/benchmarks/2026-09-02-eval-store-comparison.md). " +
		"Re-measure if any of those conditions change."
	// alphaNoteOtherTokenizer は分割器が bigram でないときに足す但し書き。
	//
	// 🔴 ADR 0015 が 0.8 を選んだ掃引は bigram で行われた。分割器を変えると
	// 語彙スコアの分布が変わるので、同じ 0.8 が同じ意味を持つ保証は無い
	// (ADR 0015 Decision 3)。**測っていないことをレポートに書かない**、を
	// ストア別の但し書きと同じ形で分割器にも適用する。
	alphaNoteOtherTokenizer = " This run did not use the bigram tokenizer: " +
		"the 0.8 default was swept on bigram only, and alpha has not been " +
		"swept for this tokenizer (ADR 0015 Decision 3, ADR 0018)."
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := start(log); err != nil {
		log.Error("evaluation failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// flags はコマンドラインの指定。
type flags struct {
	corpus  string
	queries string
	tags    string
	out     string
	alpha   float64
	limit   int
	rounds  int
	// rawOrg は -org の生の値。
	//
	// 🔴 org.ID を名乗らせない。org.NewID を通るまでは検証されていない int64 で
	// あり、ここで org.ID にすると「検証していない値が org.ID を名乗る」経路が
	// 1つ増える。生成は NewID / ParseID だけを通す (CNF-001 / CNF-002 / ADR 0003)。
	rawOrg int64
	// fusion はベクトルと語彙のスコアをどうまとめるか。
	//
	// 🔴 生の文字列で持つ。postgres.ParseFusion を通るまでは検証されていない
	// 入力であり、ここで postgres.Fusion にすると「検証していない値が
	// Fusion を名乗る」経路が1つ増える。rawOrg と同じ扱いである。
	fusion string
	// store はどのバックエンドで測るか。
	//
	// 🔴 生の文字列で持つ。config.Store にすると「検証していない値が
	// config.Store を名乗る」経路が1つ増える。rawOrg・fusion と同じ扱いである。
	store string
	// sqlitePath は -store sqlite のときに作り直すファイル。
	sqlitePath string
	// tokenizer はどの分割器で測るか。
	//
	// 🔴 生の文字列で持つ。config.Tokenizer にすると「検証していない値が
	// config.Tokenizer を名乗る」経路が1つ増える。rawOrg・fusion・store と
	// 同じ扱いである。
	tokenizer string
	// distractors は正解にならない紛れ込みの JSONL。空なら投入しない。
	//
	// 🔴 testdata/eval/ は1バイトも変えない。10万件は別のファイルとして渡し、
	// 正解注釈には一切触れない (docs/adr/0019-large-scale-benchmark-corpus.md)。
	distractors string
	// embedCache は埋め込みを貯めるディレクトリ。空なら貯めない。
	embedCache string
	// mode は候補集合の作り方。
	//
	// 🔴 生の文字列で持つ。postgres.ParseSearchMode を通るまでは検証されて
	// いない入力であり、rawOrg・fusion・store と同じ扱いである。
	mode string
	// candidateK / efSearch は -mode candidates のときの条件。
	candidateK int
	efSearch   int
	// evalDB は作り直す評価用 DB の名前。
	//
	// 🔴 このコマンドは指定された DB を DROP する。名前は
	// validateEvalDBName が recall_eval で始まるものだけに絞る。
	evalDB  string
	gpuNote string
}

// distractorSet は投入する紛れ込みと、その同一性の記録。
//
// 🔴 2つを1つの値で持つ。片方だけを運べる形にすると、投入したのに記録が
// 無いレポート（またはその逆）が作れてしまう。eval.Measure が拒否はするが、
// そもそも作れない形にしておくほうが安い。
type distractorSet struct {
	items []eval.Distractor
	// record は nil なら「紛れ込み無しで測った」を意味する。
	record *eval.FileInput
}

// embedders は計測に使う埋め込みの口。
//
// 🔴 クエリ側と取り込み側を分けて持つ。同じ Embedder を両方に使うと、
// -embed-cache を付けたときにクエリの埋め込みまでディスクから返り、
// 系統1（埋め込み往復を含む）が系統2 と同じものになる
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 3)。
type embedders struct {
	// query はクエリ側。常に素の Ollama クライアントである。
	//
	// 具体型で持つのは、環境の記録（版・モデル digest）を読む Runtime が
	// embed.Embedder の契約に無いからである。
	query *ollama.Client
	// document は取り込み側。-embed-cache があればラップされている。
	document embed.Embedder
	// cache は当たり外れの件数。使っていなければ nil。
	cache *cachingEmbedder
}

// evalInput は計測に渡す入力一式。
//
// 引数を4つ以下に保つための入れ物 (GO-011)。
type evalInput struct {
	// dataset は評価コーパス・クエリ・紛れ込み。
	dataset eval.Dataset
	// inputs は3ファイルの同一性の記録。レポートにそのまま載る。
	inputs eval.Inputs
	// distractors は紛れ込みの同一性の記録。投入していなければ nil。
	distractors *eval.FileInput
}

// session は1回の実行の入力一式。引数を4つ以下に保つための入れ物 (GO-011)。
type session struct {
	log  *slog.Logger
	cfg  config.Config
	opts flags
}

// evalTarget は評価ハーネスが要求するストアの口。
//
// 🔑 internal/eval は具体ストアを知らない層なので (ARC-001)、2つのバックエンドを
// 1つの型で扱う口はこの配線点が持つ。index.Searcher と index.Writer は契約
// パッケージのものをそのまま埋め込み、契約に無い2つ（計測用の SearchVector と
// 後始末の Close）だけをここで足す。
//
// 🔴 SearchVector を internal/index に足さない。計測の都合であって検索の契約
// ではないからである (ADR 0013)。足すと、すべてのストア実装が計測の都合に
// 付き合わされる。
type evalTarget interface {
	index.Searcher
	index.Writer

	// SearchVector は埋め込み済みのベクトルで検索する。系統2 の計測対象。
	SearchVector(ctx context.Context, q index.Query, vector []float32) ([]index.Result, error)
	// Close は接続を閉じる。
	Close() error
}

// evalStore は評価用の接続とストア。
//
// 環境の記録（エンジンの版）を読むのに素の *sql.DB が要るので、ストアと
// 一緒に持ち回る。ranking を一緒に持つのは、具体ストアの型が分かるのが
// ここだけだからである——組み立てた場所で eval の形へ写し替えておかないと、
// 呼び出し側で型を判別し直すことになる。
type evalStore struct {
	store   evalTarget
	db      *sql.DB
	ranking eval.RankingSettings
}

// serverVersions は DB 側の版。
//
// どちらのストアで測っても、使ったエンジンの版だけが埋まる。空の項目は
// 「そのエンジンでは測っていない」を意味する。
type serverVersions struct {
	postgres string
	pgvector string
	sqlite   string
}

// buildStamp はバイナリに埋まったビルド情報。
type buildStamp struct {
	revision  string
	modified  bool
	goVersion string
}

// start はフラグと設定を読み、評価を実行する。
func start(log *slog.Logger) error {
	opts, err := parseFlags()
	if err != nil {
		return err
	}

	// 🔴 cfg を構造体ごとログに出さないこと。config.Config は String() を
	// 実装していないので、%v や slog.Any に渡すと VoyageAPIKey がそのまま出る。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout)
	defer cancel()

	return session{log: log, cfg: cfg, opts: opts}.run(ctx)
}

// parseFlags はコマンドラインを読む。
func parseFlags() (flags, error) {
	var opts flags

	flag.StringVar(&opts.corpus, "corpus", "testdata/eval/corpus.jsonl", "評価コーパス (JSONL)")
	flag.StringVar(&opts.queries, "queries", "testdata/eval/queries.jsonl", "評価クエリ (JSONL)")
	flag.StringVar(&opts.tags, "tags", "testdata/eval/tags.json", "タグ語彙 (JSON)")
	flag.StringVar(&opts.out, "out", "", "レポートの書き出し先 (JSON・必須)")
	flag.Float64Var(&opts.alpha, "alpha", alphaFromConfig,
		"合成の重み。未指定なら RECALL_DEFAULT_ALPHA を使う（既定 0.8 の根拠は ADR 0015・条件付き）")
	flag.IntVar(&opts.limit, "limit", eval.DefaultLimit, "1クエリあたりの取得件数")
	flag.IntVar(&opts.rounds, "rounds", eval.DefaultRounds, "各クエリを繰り返す回数")
	flag.Int64Var(&opts.rawOrg, "org", 1, "投入・検索に使う org_id")
	flag.StringVar(&opts.fusion, "fusion", postgres.FusionWeightedSum.String(),
		"合成方式: "+strings.Join(postgres.FusionNames(), " | ")+
			"（rrf は alpha を無視する。既定は加重和。postgres でのみ指定できる）")
	flag.StringVar(&opts.store, "store", storeNamePostgres,
		"計測するバックエンド: "+storeNamePostgres+" | "+storeNameSQLite+
			"（既定は postgres。sqlite は比較実測用・ADR 0017）")
	flag.StringVar(&opts.sqlitePath, "sqlite-path", defaultSQLitePath,
		"-store sqlite のときに作り直すファイル")
	flag.StringVar(&opts.tokenizer, "tokenizer", tokenizerNameBigram,
		"語彙分割器: "+tokenizerNameList+
			"（既定は bigram。kagome は比較実測用・ADR 0018。union は両者の連結・ADR 0021）")
	flag.StringVar(&opts.distractors, "distractors", "",
		"正解にならない紛れ込みの JSONL（省略可・ADR 0019）。"+
			"評価コーパスの投入後にバッチで投入する。正解注釈には一切触れない")
	flag.StringVar(&opts.embedCache, "embed-cache", "",
		"埋め込みを貯めるディレクトリ（省略可・ADR 0019）。"+
			"クエリ側はキャッシュしないので系統1 の latency は変わらない")
	flag.StringVar(&opts.mode, "mode", postgres.SearchModeExhaustive.String(),
		"候補集合の作り方: "+strings.Join(postgres.SearchModeNames(), " | ")+
			"（既定は全探索。candidates は索引を効かせる計測モード・ADR 0022。postgres でのみ指定できる）")
	flag.IntVar(&opts.candidateK, "candidate-k", postgres.DefaultCandidateK,
		"-mode candidates のときの両側 top-K（既定 100 に根拠は無い・ADR 0022）")
	flag.IntVar(&opts.efSearch, "ef-search", postgres.DefaultEfSearch,
		"-mode candidates のときの hnsw.ef_search（既定 40 は pgvector の既定。K 以上であること）")
	flag.StringVar(&opts.evalDB, "eval-db", defaultEvalDBName,
		"作り直す評価用 DB の名前（"+defaultEvalDBName+" で始まること）。"+
			"複数のレーンが同時に測るとき、共有の "+defaultEvalDBName+" を壊し合わないために分ける")
	flag.StringVar(&opts.gpuNote, "gpu-note", "",
		"GPU の占有状況などの自己申告。レポートにそのまま載る")
	flag.Parse()

	if opts.out == "" {
		return flags{}, fmt.Errorf("%w: -out is required", errFlags)
	}

	if err := validateEvalDBName(opts.evalDB); err != nil {
		return flags{}, err
	}

	return opts, nil
}

// validateEvalDBName は DROP してよい名前だけを通す。
//
// 🔴 正規表現 ^recall_eval[a-z0-9_]*$ と同じものを手で書いている。
// regexp.MustCompile はパッケージ変数に置けば GO-007（可変グローバル禁止）に、
// 関数内に置けば呼び出しごとの再コンパイルと panic 経路に触れる。
// 20 行に満たない検査に、そのどちらの対価も払わない。
func validateEvalDBName(name string) error {
	suffix, found := strings.CutPrefix(name, defaultEvalDBName)
	if !found {
		return fmt.Errorf("%w: %q must start with %q", errEvalDBName, name, defaultEvalDBName)
	}

	for _, r := range suffix {
		if !isEvalDBNameRune(r) {
			return fmt.Errorf("%w: %q contains %q (only lowercase letters, digits and _ may follow %q)",
				errEvalDBName, name, string(r), defaultEvalDBName)
		}
	}

	return nil
}

// isEvalDBNameRune は接頭辞に続けてよい文字かを返す。
func isEvalDBNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
}

// run は評価セットを読み、計測し、レポートを書く。
func (s session) run(ctx context.Context) error {
	in, err := loadInput(s.opts)
	if err != nil {
		return err
	}

	emb, err := s.buildEmbedders()
	if err != nil {
		return err
	}

	target, err := s.openEvalStore(ctx, emb.document)
	if err != nil {
		return err
	}

	defer func() {
		if err := target.store.Close(); err != nil {
			s.log.Error("failed to close the store", slog.Any("error", err))
		}
	}()

	measurement, err := s.measure(ctx, target, emb.query, in)
	if err != nil {
		return err
	}

	s.logCache(emb.cache)

	environment, err := s.environment(ctx, target, emb.query)
	if err != nil {
		return err
	}

	report := eval.NewReport(environment, in.inputs, measurement, time.Now())
	if err := writeReport(s.opts.out, report); err != nil {
		return err
	}

	s.logSummary(report)

	return nil
}

// buildEmbedder は設定から Ollama クライアントを組み立てる。
//
// voyage は評価でも未対応である。既定構成（ローカル・費用0円）で測ることが
// 前提なので、外部 API 経路をここで開けない (ADR 0008)。
func (s session) buildEmbedder() (*ollama.Client, error) {
	if s.cfg.EmbedProvider != config.EmbedProviderOllama {
		return nil, fmt.Errorf("%w: got %q", errEmbedderNotSupported, s.cfg.EmbedProvider)
	}

	client, err := ollama.New(ollama.Config{
		BaseURL:    s.cfg.OllamaBaseURL,
		Model:      s.cfg.EmbedModel,
		Dimensions: s.cfg.EmbedDimensions,
		BatchSize:  ollama.DefaultBatchSize,
		HTTPClient: &http.Client{
			Transport:     nil, // 既定の Transport を使う
			CheckRedirect: nil, // 既定のリダイレクト方針
			Jar:           nil, // Cookie を持たない
			Timeout:       ollamaTimeout,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build ollama embedder: %w", err)
	}

	return client, nil
}

// openEvalStore は評価専用の入れ物を作り直し、移行済みで空のストアを返す。
//
// 🔴 開発用の recall にもテスト用の recall_test にも相乗りしない。無関係な行が
// 1件でも混ざれば、それは順位に入り込んで recall を汚染する。症状は
// 「recall が少し低い」だけなので気づけない (ADR 0013)。SQLite でも同じで、
// 前回のファイルを消してから作り直す。
func (s session) openEvalStore(ctx context.Context, embedder embed.Embedder) (evalStore, error) {
	switch s.opts.store {
	case storeNamePostgres:
		return s.openPostgresStore(ctx, embedder)
	case storeNameSQLite:
		return s.openSQLiteStore(ctx, embedder)
	}

	return evalStore{}, fmt.Errorf("%w: %w: %q (want %q or %q)",
		errFlags, errUnknownStore, s.opts.store, storeNamePostgres, storeNameSQLite)
}

// buildTokenizer は -tokenizer から語彙分割器を組み立てる。
//
// 🔴 未知の名前を既定へ黙って倒さない。倒すと「kagome で測った」と記録された
// レポートが bigram の数字を持つことになり、ADR 0018 が比較したいものを
// 比較できなくなる。postgres.ParseFusion・openEvalStore と同じ判断である。
//
// ⚠️ bigram.New だけが error を返さない（辞書の読み込みを含まないため）。契約
// (lexical.Tokenizer) の違いではない。
func buildTokenizer(name string) (lexical.Tokenizer, error) {
	switch name {
	case tokenizerNameBigram:
		return bigram.New(), nil
	case tokenizerNameKagome:
		morphological, err := kagome.New()
		if err != nil {
			return nil, fmt.Errorf("build kagome tokenizer: %w", err)
		}

		return morphological, nil
	case tokenizerNameUnion:
		both, err := union.New()
		if err != nil {
			return nil, fmt.Errorf("build union tokenizer: %w", err)
		}

		return both, nil
	}

	return nil, fmt.Errorf("%w: %w: %q (want %s)",
		errFlags, errUnknownTokenizer, name, tokenizerNameList)
}

// openPostgresStore は評価専用 DB を作り直して繋ぐ。
func (s session) openPostgresStore(
	ctx context.Context, embedder embed.Embedder,
) (evalStore, error) {
	// 🔴 融合方式はここで解釈する。未知の指定を既定へ黙って倒さないので、
	// 綴り誤りは「既定で測った」結果として記録されずに止まる。
	fusion, err := postgres.ParseFusion(s.opts.fusion)
	if err != nil {
		return evalStore{}, fmt.Errorf("%w: %w", errFlags, err)
	}

	// 🔴 候補の作り方もここで解釈する。既定へ黙って倒すと、綴り誤りが
	// 「exhaustive で測った」結果として記録され、索引の after を取り違える
	// (docs/adr/0022-indexed-candidate-search.md Decision 3)。
	mode, err := postgres.ParseSearchMode(s.opts.mode)
	if err != nil {
		return evalStore{}, fmt.Errorf("%w: %w", errFlags, err)
	}

	tokenizer, err := buildTokenizer(s.opts.tokenizer)
	if err != nil {
		return evalStore{}, err
	}

	if err := recreateEvalDatabase(ctx, s.opts.evalDB); err != nil {
		return evalStore{}, err
	}

	db, err := postgres.Open(ctx, evalDSN(s.opts.evalDB))
	if err != nil {
		return evalStore{}, fmt.Errorf("open evaluation database: %w", err)
	}

	store, err := postgres.New(db, postgres.Options{
		Embedder:   embedder,
		Tokenizer:  tokenizer,
		Fusion:     fusion,
		SearchMode: mode,
		CandidateK: s.opts.candidateK,
		EfSearch:   s.opts.efSearch,
	})
	if err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("build store: %w", err), db.Close())
	}

	if err := store.Migrate(ctx); err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("migrate: %w", err), store.Close())
	}

	return evalStore{store: store, db: db, ranking: postgresRanking(store.RankingSettings())}, nil
}

// postgresRanking はストアの条件をレポートの形へ写す。
//
// 🔴 ポインタで渡すのは「無い」と「0」を区別するためである。ts_rank の
// 正規化フラグは 0 が実際に使っている値なので、値のコピーを1つ取って
// その番地を渡す。settings のフィールドを直接指さないのは、ローカル変数の
// 寿命を明示して「後で書き換わらない値」にしておくためである。
//
// 🔑 CandidateK と EfSearch はストアが既にポインタで返す（exhaustive では nil）。
// ここで nil を 0 に潰さないこと——潰した瞬間に「K=0 で測った」というありえない
// 条件がレポートに載る (ADR 0022 Decision 3・様式 v7)。
func postgresRanking(settings postgres.RankingSettings) eval.RankingSettings {
	tsRank := settings.TsRankNormalization
	rrfK := settings.RRFK
	mode := settings.SearchMode

	return eval.RankingSettings{
		Fusion:              settings.Fusion,
		Store:               settings.Store,
		LexicalScorer:       settings.LexicalScorer,
		TokenizerID:         settings.TokenizerID,
		TsRankNormalization: &tsRank,
		RRFK:                &rrfK,
		SearchMode:          &mode,
		CandidateK:          settings.CandidateK,
		EfSearch:            settings.EfSearch,
	}
}

// openSQLiteStore は評価専用のファイルを作り直して繋ぐ。
//
// 🔴 -fusion を受け付けない。SQLite 側は加重和しか実装していないので、
// rrf を指定されたら黙って加重和に倒さずエラーにする (ADR 0017 Decision 4)。
// 倒すと「rrf で測った」と記録されたレポートが加重和の数字を持つことになる。
func (s session) openSQLiteStore(
	ctx context.Context, embedder embed.Embedder,
) (evalStore, error) {
	if s.opts.fusion != postgres.FusionWeightedSum.String() {
		return evalStore{}, fmt.Errorf(
			"%w: -store %s does not support -fusion %q (only %q is implemented)",
			errFlags, storeNameSQLite, s.opts.fusion, postgres.FusionWeightedSum.String())
	}

	// 🔴 -mode candidates も受け付けない。索引つきの候補生成は Postgres の
	// 話であって、SQLite は ADR 0022 の対象外である (Decision 5)。黙って
	// exhaustive に倒すと、「candidates で測った」と記録されたレポートが
	// 全探索の数字を持つことになる——-fusion を拒否しているのと同じ理由。
	if s.opts.mode != postgres.SearchModeExhaustive.String() {
		return evalStore{}, fmt.Errorf(
			"%w: -store %s does not support -mode %q (ADR 0022 covers postgres only)",
			errFlags, storeNameSQLite, s.opts.mode)
	}

	tokenizer, err := buildTokenizer(s.opts.tokenizer)
	if err != nil {
		return evalStore{}, err
	}

	if err := recreateEvalFile(s.opts.sqlitePath); err != nil {
		return evalStore{}, err
	}

	db, err := sqlite.Open(ctx, s.opts.sqlitePath)
	if err != nil {
		return evalStore{}, fmt.Errorf("open evaluation database: %w", err)
	}

	store, err := sqlite.New(db, embedder, tokenizer)
	if err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("build store: %w", err), db.Close())
	}

	if err := store.Migrate(ctx); err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("migrate: %w", err), store.Close())
	}

	settings := store.RankingSettings()

	return evalStore{store: store, db: db, ranking: eval.RankingSettings{
		Fusion:        settings.Fusion,
		Store:         settings.Store,
		LexicalScorer: settings.LexicalScorer,
		TokenizerID:   settings.TokenizerID,
		// 🔴 postgres 専用の2項目は nil にする。JSON からはキーごと消え、
		// レポートは「この採点関数にそのつまみが無い」と読める。0 を入れると
		// 「フラグ 0 で測った」と読まれ、SQLite に無い ts_rank の条件が
		// 記録されたことになる（v3 までがそうだった・様式 v4 で塞いだ）。
		TsRankNormalization: nil,
		RRFK:                nil,
		// 🔴 候補の作り方も同じ扱いである。SQLite に索引つきの候補生成は
		// 無いので、"exhaustive" と書くと「全探索という選択をした」と読める。
		// 選択肢が1つしか無いことと、2つのうち片方を選んだことは違う
		// (ADR 0022 Decision 5・様式 v7)。
		SearchMode: nil,
		CandidateK: nil,
		EfSearch:   nil,
	}}, nil
}

// recreateEvalFile は評価用の SQLite ファイルを消して作り直す。
//
// 🔴 -wal と -shm も一緒に消す。本体だけ消すと、前回のジャーナルが残った
// ファイルを開くことになり、消したはずの行が復活しうる。
//
// 🔑 「作り直す」の実体は削除だけである。ファイルは Open が作り、スキーマは
// Migrate が作る。postgres 側の DROP DATABASE / CREATE DATABASE と同じ意味。
func recreateEvalFile(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: remove %s: %w", errEvalDatabase, path+suffix, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), reportDirMode); err != nil {
		return fmt.Errorf("%w: create directory: %w", errEvalDatabase, err)
	}

	return nil
}

// recreateEvalDatabase は評価用 DB を落として作り直す。
//
// 作り直すのは、前回の残骸に依存した計測が「たまたま良い数字を出す」状態を
// 作らないため。internal/store/postgres/main_test.go の流儀を踏襲している。
func recreateEvalDatabase(ctx context.Context, dbName string) error {
	// ドライバ名は pgx。postgres パッケージが登録するのと同じ値である。
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return fmt.Errorf("%w: open admin connection: %w", errEvalDatabase, err)
	}

	if err := resetDatabase(ctx, admin, dbName); err != nil {
		return errors.Join(err, admin.Close())
	}

	if err := admin.Close(); err != nil {
		return fmt.Errorf("%w: close admin connection: %w", errEvalDatabase, err)
	}

	return nil
}

// resetDatabase は DROP して CREATE する。
//
// DB 名は識別子なのでプレースホルダにできず、ここは文字列連結になる。
// 🔴 だからこそ dbName は validateEvalDBName を通ったものでなければならない。
// parseFlags がフラグを読んだ直後に通しているので、この関数へ届く名前は
// recall_eval で始まり [a-z0-9_] だけが続く——開発用の recall にも
// テスト用の recall_test_<pid> にも当たらない形に閉じている。
func resetDatabase(ctx context.Context, admin *sql.DB, dbName string) error {
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: cannot reach postgres, run `docker compose up -d`: %w",
			errEvalDatabase, err)
	}

	// FORCE は残った接続を切ってから落とす（PostgreSQL 13 以降）。
	if _, err := admin.ExecContext(ctx,
		`DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("%w: drop %s: %w", errEvalDatabase, dbName, err)
	}

	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+dbName); err != nil {
		return fmt.Errorf("%w: create %s: %w", errEvalDatabase, dbName, err)
	}

	return nil
}

// measure は計測ループを組み立てて走らせる。
func (s session) measure(
	ctx context.Context, target evalStore, embedder embed.Embedder, in evalInput,
) (eval.Measurement, error) {
	runner, err := eval.NewRunner(eval.Dependencies{
		Writer:         target.store,
		Searcher:       target.store,
		VectorSearcher: target.store,
		EmbedQuery:     embedQuery(embedder),
	})
	if err != nil {
		return eval.Measurement{}, fmt.Errorf("build runner: %w", err)
	}

	options, err := s.measureOptions(target, in.distractors)
	if err != nil {
		return eval.Measurement{}, err
	}

	measurement, err := runner.Measure(ctx, in.dataset, options)
	if err != nil {
		return eval.Measurement{}, fmt.Errorf("measure: %w", err)
	}

	return measurement, nil
}

// measureOptions は計測条件を組み立てる。
//
// 🔴 条件はストアに聞く。フラグの値をそのまま書き写すと、既定を変えたときに
// 「指定したつもりの条件」と「実際に使われた条件」がずれる。レポートは後者で
// なければ意味が無い。ranking は組み立てた場所で写し替えてあるので運ぶだけで、
// alpha_note も同じ理由で target.ranking.Store から選ぶ。
func (s session) measureOptions(
	target evalStore, distractors *eval.FileInput,
) (eval.Options, error) {
	orgID, err := org.NewID(s.opts.rawOrg)
	if err != nil {
		return eval.Options{}, fmt.Errorf("org id: %w", err)
	}

	alpha, err := s.alpha()
	if err != nil {
		return eval.Options{}, err
	}

	note, err := alphaNote(target.ranking.Store)
	if err != nil {
		return eval.Options{}, err
	}

	return eval.Options{
		OrgID:     orgID,
		Alpha:     alpha,
		AlphaNote: note + tokenizerNote(target.ranking.TokenizerID),
		Limit:     s.opts.limit,
		Rounds:    s.opts.rounds,
		Ranking:   target.ranking,
		// 🔴 フラグの値ではなく、実際に読んだファイルの記録を渡す。
		// -distractors を指定してもファイルが空なら LoadDistractorFile が
		// 止めるので、ここに来る record は必ず投入されるものと一致する。
		Distractors: distractors,
		EmbedCache:  s.opts.embedCache != "",
	}, nil
}

// alphaNote は測ったストアに対応する但し書きを選ぶ。
//
// 🔴 未知の名前を既定へ黙って倒さない。倒すと SQLite のレポートに Postgres の
// 但し書きが載り、v4 で塞いだはずの読み違えがそのまま戻る。errUnknownStore を
// 使うのは openEvalStore と同じ理由である。
func alphaNote(store string) (string, error) {
	switch store {
	case storeNamePostgres:
		return alphaNotePostgres, nil
	case storeNameSQLite:
		return alphaNoteSQLite, nil
	}

	return "", fmt.Errorf("%w: %w: %q (want %q or %q)",
		errFlags, errUnknownStore, store, storeNamePostgres, storeNameSQLite)
}

// tokenizerNote は分割器が bigram でないときの但し書きを返す。
//
// 🔴 判定はフラグの値ではなく、ストアが実際に使った Tokenizer.ID() で行う。
// conditions.ranking を「指定したつもりの条件」ではなく「実際に使われた条件」に
// 揃えているのと同じ理由である（様式 v4）。
//
// 🔑 bigram の識別子を実物から引くのは、リテラルで書くと分割規則の版を上げた
// ときに（"bigram:nfkc-lower:v2"）静かに但し書きが付き始めるからである。
func tokenizerNote(tokenizerID string) string {
	if tokenizerID == bigram.New().ID() {
		return ""
	}

	return alphaNoteOtherTokenizer
}

// alpha は -alpha が未指定なら設定の既定値を使う。
//
// 🔴 float64 で返す。レポートに刻むのは float32 へ落とす前の10進である
// (ADR 0013 の「集計値を第三者が再計算できる」は、条件の値が機械で
// 突き合わせられることを含む)。
//
// ⚠️ 既定 0.8 は ADR 0015 が実測から選んだ値だが、測ったときの条件に紐づく
// 条件付きの値である。-alpha で明示した値のほうは、掃引の1点でしかない。
// どちらもレポートの alpha_note が但し書きを付けて回る。
func (s session) alpha() (float64, error) {
	if s.opts.alpha < 0 {
		return decimalOfFloat32(s.cfg.DefaultAlpha)
	}

	return s.opts.alpha, nil
}

// decimalOfFloat32 は float32 の値を、それを生んだ10進表記に戻して float64 にする。
//
// 🔴 float64(float32(0.8)) は 0.8000000119209290 になる。config.DefaultAlpha は
// float32 なので、そのまま広げるとレポートに刻まれる値が設定に書いた 10進と
// 一致しない。FormatFloat(..., 32) が返すのは「同じ float32 に戻る最短の10進」
// なので、これを経由すれば設定の 0.8 が 0.8 のまま復元される。
//
// 🔑 config.DefaultAlpha を float64 に広げないのは、それが index.Query.Alpha
// (float32・契約) と httpapi の既定値でもあるからである。型を広げる判断は
// この修正の射程ではない。
func decimalOfFloat32(v float32) (float64, error) {
	f, err := strconv.ParseFloat(strconv.FormatFloat(float64(v), 'f', -1, 32), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: alpha %v is not a finite decimal: %w", errFlags, v, err)
	}

	return f, nil
}

// embedQuery は Embedder を eval が要求する関数型に適合させる。
//
// 🔴 Kind は KindQuery。Store.Search が内部で使うのと同じ Kind でなければ、
// 2系統が別のベクトルを測ることになり、latency を並べる意味が消える
// （プロバイダによっては接頭辞やパラメータが変わる・ADR 0008）。
func embedQuery(embedder embed.Embedder) eval.EmbedQuery {
	return func(ctx context.Context, text string) ([]float32, error) {
		vectors, err := embedder.Embed(ctx, []string{text}, embed.KindQuery)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}

		if len(vectors) != 1 {
			return nil, fmt.Errorf("%w: got %d for 1 query", errEmbedCount, len(vectors))
		}

		return vectors[0], nil
	}
}

// environment は再現に要る環境の記録を集める。
//
// 🔴 Ollama の版とモデル digest を取れなければ失敗にする。埋め込みができている
// のに素性が取れない状況は考えにくく、素性の欠けたレポートは後から検証できない
// （docs/benchmarks/2026-09-01-baseline.md の追記が残した教訓）。
func (s session) environment(
	ctx context.Context, target evalStore, embedder *ollama.Client,
) (eval.Environment, error) {
	runtime, err := embedder.Runtime(ctx)
	if err != nil {
		return eval.Environment{}, fmt.Errorf("%w: ollama runtime: %w", errEnvironment, err)
	}

	versions, err := s.readServerVersions(ctx, target.db)
	if err != nil {
		return eval.Environment{}, err
	}

	stamp := readBuildStamp()

	return eval.Environment{
		GitRevision:     stamp.revision,
		GitModified:     stamp.modified,
		GoVersion:       stamp.goVersion,
		EmbedderID:      embedder.ID(),
		OllamaVersion:   runtime.Version,
		ModelDigest:     runtime.Digest,
		PostgresVersion: versions.postgres,
		PgvectorVersion: versions.pgvector,
		SQLiteVersion:   versions.sqlite,
		GPUNote:         s.opts.gpuNote,
	}, nil
}

// readServerVersions は測ったエンジンの版を読む。
//
// 🔴 測っていないほうのエンジンには問い合わせない。SQLite の接続に
// SELECT version() を投げても pg_extension は無く、失敗するだけである。
// 「取れなかった」を空で埋めるのではなく、そもそも問わない。
func (s session) readServerVersions(ctx context.Context, db *sql.DB) (serverVersions, error) {
	if s.opts.store == storeNameSQLite {
		return readSQLiteVersion(ctx, db)
	}

	return readPostgresVersions(ctx, db)
}

// readPostgresVersions は PostgreSQL と pgvector の版を読む。
func readPostgresVersions(ctx context.Context, db *sql.DB) (serverVersions, error) {
	var pg string
	if err := db.QueryRowContext(ctx, `SELECT version()`).Scan(&pg); err != nil {
		return serverVersions{}, fmt.Errorf("%w: postgres version: %w", errEnvironment, err)
	}

	var vector string

	err := db.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&vector)
	if err != nil {
		return serverVersions{}, fmt.Errorf("%w: pgvector version: %w", errEnvironment, err)
	}

	return serverVersions{postgres: pg, pgvector: vector, sqlite: ""}, nil
}

// readSQLiteVersion は SQLite の版を読む。
//
// 🔴 取れなければ失敗にする。素性の欠けたレポートは後から検証できない、と
// いうのは Ollama の版に対する判断と同じである。
func readSQLiteVersion(ctx context.Context, db *sql.DB) (serverVersions, error) {
	var version string
	if err := db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		return serverVersions{}, fmt.Errorf("%w: sqlite version: %w", errEnvironment, err)
	}

	return serverVersions{postgres: "", pgvector: "", sqlite: version}, nil
}

// readBuildStamp は git の版と未コミット変更の有無を読む。
//
// 🔴 go run では埋まらない（2026-09-01 実測）。go build したバイナリにしか
// vcs.revision は入らないので、make eval はビルドしてから走らせている。
//
// ⚠️ それでも取れないことがある（-buildvcs=false でビルドした場合など）。
// そのときは空で残す。空であること自体が「どのコードで測ったか分からない」
// という情報である。
func readBuildStamp() buildStamp {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildStamp{revision: "", modified: false, goVersion: ""}
	}

	stamp := buildStamp{revision: "", modified: false, goVersion: info.GoVersion}

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			stamp.revision = setting.Value
		case "vcs.modified":
			stamp.modified = setting.Value == "true"
		}
	}

	return stamp
}

// loadInput は評価セットと紛れ込みを読み、計測へ渡す形にする。
//
// 🔴 eval.LoadDataset は整合性の検査まで済ませて返す。dangling な正解キーを
// 抱えたまま測ると、症状は「recall が低い」だけになり、原因が注釈だと分からない。
func loadInput(opts flags) (evalInput, error) {
	data, err := loadDataset(opts)
	if err != nil {
		return evalInput{}, err
	}

	set, err := loadDistractors(opts.distractors)
	if err != nil {
		return evalInput{}, err
	}

	// 🔴 紛れ込みは Dataset の別の欄に入れる。Passages に混ぜないので、
	// 正解注釈の検査は 259 件だけを見たままである
	// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 2)。
	data.Dataset.Distractors = set.items

	return evalInput{
		dataset:     data.Dataset,
		inputs:      data.Inputs,
		distractors: set.record,
	}, nil
}

// loadDistractors は -distractors のファイルを読む。指定が無ければ空を返す。
func loadDistractors(path string) (distractorSet, error) {
	if path == "" {
		return distractorSet{items: nil, record: nil}, nil
	}

	source, err := readSource(path)
	if err != nil {
		return distractorSet{}, err
	}

	items, input, err := eval.LoadDistractorFile(source)
	if err != nil {
		return distractorSet{}, fmt.Errorf("load distractors: %w", err)
	}

	return distractorSet{items: items, record: &input}, nil
}

// buildEmbedders は計測に使う2つの口を組み立てる。
//
// 🔴 1つの関数で両方を返すのは、「クエリ側は素・取り込み側はラップ」という
// 対応を配線の1箇所で決め切るためである。別々に組み立てられる形にすると、
// 呼び出し側の取り違えでクエリ側にキャッシュが入り、系統1 の latency が
// 埋め込み往復を含まなくなる (ADR 0019 Decision 3)。
func (s session) buildEmbedders() (embedders, error) {
	client, err := s.buildEmbedder()
	if err != nil {
		return embedders{}, err
	}

	if s.opts.embedCache == "" {
		return embedders{query: client, document: client, cache: nil}, nil
	}

	cache, err := newCachingEmbedder(client, s.opts.embedCache)
	if err != nil {
		return embedders{}, err
	}

	return embedders{query: client, document: cache, cache: cache}, nil
}

// logCache はキャッシュの当たり外れを標準エラーへ出す。
//
// 🔴 標準出力ではない。stdout には計測の要約が JSON で出るので、道具の
// 動作記録を混ぜない。キャッシュの件数は計測結果ではなく、実行の様子である。
func (s session) logCache(cache *cachingEmbedder) {
	if cache == nil {
		return
	}

	slog.New(slog.NewJSONHandler(os.Stderr, nil)).Info("embedding cache",
		slog.String("dir", s.opts.embedCache),
		slog.Int64("hits", cache.Hits()),
		slog.Int64("misses", cache.Misses()),
	)
}

// loadDataset は3つのファイルを読んで評価セットにする。
//
// 開くのがここの仕事で、解析・ハッシュ・整合性の検査は internal/eval が持つ
// （配線点に置くとテストが書けず、レポートの正しさを支える部分が検査されない）。
func loadDataset(opts flags) (eval.LoadedDataset, error) {
	corpus, err := readSource(opts.corpus)
	if err != nil {
		return eval.LoadedDataset{}, err
	}

	queries, err := readSource(opts.queries)
	if err != nil {
		return eval.LoadedDataset{}, err
	}

	tags, err := readSource(opts.tags)
	if err != nil {
		return eval.LoadedDataset{}, err
	}

	loaded, err := eval.LoadDataset(corpus, queries, tags)
	if err != nil {
		return eval.LoadedDataset{}, fmt.Errorf("load evaluation set: %w", err)
	}

	return loaded, nil
}

// readSource は評価セットのファイルを1つ読む。
func readSource(path string) (eval.SourceFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return eval.SourceFile{}, fmt.Errorf("%w: %s: %w", errDataset, path, err)
	}

	return eval.SourceFile{Path: path, Content: raw}, nil
}

// writeReport はレポートを書き出す。
func writeReport(path string, report eval.Report) error {
	encoded, err := eval.EncodeReport(report)
	if err != nil {
		return fmt.Errorf("%w: %w", errReport, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), reportDirMode); err != nil {
		return fmt.Errorf("%w: create directory: %w", errReport, err)
	}

	if err := os.WriteFile(path, encoded, reportFileMode); err != nil {
		return fmt.Errorf("%w: %s: %w", errReport, path, err)
	}

	return nil
}

// logSummary は主要な数字だけを標準出力へ出す。全量はレポートにある。
//
// 2系統の p95 を必ず両方出す。片方だけ報告しないこと (ADR 0009)。
func (s session) logSummary(report eval.Report) {
	summary := report.Summary

	s.log.Info("evaluation finished",
		slog.String("out", s.opts.out),
		slog.Int("queries", summary.QueryCount),
		slog.Float64("recall_at_1", eval.RecallValueAt(summary.Recall, 1)),
		slog.Float64("recall_at_5", eval.RecallValueAt(summary.Recall, 5)),
		slog.Float64("recall_at_10", eval.RecallValueAt(summary.Recall, 10)),
		slog.Float64("mrr", summary.MRR),
		// 🔴 alpha を必ず出す。掃引しているときに、どの条件の数字を見ているのか
		// 端末の出力だけで分からないと取り違える。
		slog.Float64("alpha", report.Conditions.Alpha),
		// 🔴 融合方式を必ず出す。alpha だけでは条件が決まらない
		// （順位融合では alpha は無視される）。
		slog.String("fusion", report.Conditions.Ranking.Fusion),
		// 🔴 バックエンドと語彙採点関数も出す。2つのストアを続けて測るときに、
		// 端末の出力だけでどちらの数字か分からないと取り違える (ADR 0017)。
		slog.String("store", report.Conditions.Ranking.Store),
		slog.String("lexical_scorer", report.Conditions.Ranking.LexicalScorer),
		// 🔴 分割器も出す。bigram と kagome を続けて測るときに、端末の出力だけで
		// どちらの数字か分からないと取り違える (ADR 0018)。
		slog.String("tokenizer_id", report.Conditions.Ranking.TokenizerID),
		// 🔴 候補の作り方も出す。索引の before/after を続けて測るときに、
		// 端末の出力だけでどちらの数字か分からないと取り違える (ADR 0022)。
		slog.String("search_mode", searchModeOf(report.Conditions.Ranking)),
		// micro は正解チャンク単位の内訳。クエリ単位のマクロ平均とは別物で、
		// 「どのチャンクが拾えていないか」を見るにはこちらが要る。
		slog.Float64("micro_recall", summary.MicroRecall.Value),
		// 名指しの長文 gold がどれだけ拾えたか。Q-1 の交絡要因の見張りである。
		slog.Float64("long_chunk_recall", summary.LongChunkRecall.Value),
		// 🔴 紛れ込みの件数を必ず出す。259 件だけで測った数字と 10万件の中で
		// 測った数字は、端末の出力では区別がつかない (ADR 0019 Decision 2)。
		slog.Int("distractors", distractorCount(report.Conditions.Distractors)),
		slog.Float64("p95_with_embedding_ms", summary.Latency.WithEmbedding.P95MS),
		slog.Float64("p95_without_embedding_ms", summary.Latency.WithoutEmbedding.P95MS),
		slog.String("model_digest", report.Environment.ModelDigest),
	)
}

// searchModeOf は記録から候補の作り方を取り出す。
//
// nil は「そのストアに候補の作り方という概念が無い」を意味する（sqlite）。
// 端末では "n/a" と出す——"exhaustive" と書くと、選択肢が1つしか無いことと
// 2つのうち片方を選んだことが区別できなくなる (ADR 0022 Decision 5)。
func searchModeOf(ranking eval.RankingSettings) string {
	if ranking.SearchMode == nil {
		return "n/a"
	}

	return *ranking.SearchMode
}

// distractorCount は記録から紛れ込みの件数を取り出す。
//
// nil は「紛れ込み無しで測った」を意味するので 0 になる。
func distractorCount(record *eval.FileInput) int {
	if record == nil {
		return 0
	}

	return record.Count
}
