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
	"time"

	// 評価用 DB の作り直しにも pgx を使う。blank import なのは、
	// 必要なのが database/sql への登録だけだからである。
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
	"github.com/hideyukiMORI/nene-recall/internal/eval"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
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
const (
	adminDSN   = "postgres://recall:recall@localhost:5433/recall?sslmode=disable"
	evalDSN    = "postgres://recall:recall@localhost:5433/recall_eval?sslmode=disable"
	evalDBName = "recall_eval"
)

// evalTimeout は計測全体の上限。
//
// コーパスの投入（埋め込みを含む）とクエリ数 ×（1 + ラウンド数）回の検索を
// 収める必要がある。実測 87.8 件/秒 の埋め込みで 1,000 チャンクなら約 11 秒、
// 検索は 1回 50〜100ms 程度なので、数十クエリなら数分で終わる。
// 30 分は「無期限にはしない」ための上限であって、目標値ではない。
const evalTimeout = 30 * time.Minute

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
	rawOrg  int64
	gpuNote string
}

// session は1回の実行の入力一式。引数を4つ以下に保つための入れ物 (GO-011)。
type session struct {
	log  *slog.Logger
	cfg  config.Config
	opts flags
}

// evalStore は評価用の接続とストア。
//
// 環境の記録（PostgreSQL と pgvector の版）を読むのに素の *sql.DB が要るので、
// ストアと一緒に持ち回る。
type evalStore struct {
	store *postgres.Store
	db    *sql.DB
}

// serverVersions は DB 側の版。
type serverVersions struct {
	postgres string
	pgvector string
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
		"合成の重み。未指定なら RECALL_DEFAULT_ALPHA を使う（根拠はまだ無い・要件定義 Q-3）")
	flag.IntVar(&opts.limit, "limit", eval.DefaultLimit, "1クエリあたりの取得件数")
	flag.IntVar(&opts.rounds, "rounds", eval.DefaultRounds, "各クエリを繰り返す回数")
	flag.Int64Var(&opts.rawOrg, "org", 1, "投入・検索に使う org_id")
	flag.StringVar(&opts.gpuNote, "gpu-note", "",
		"GPU の占有状況などの自己申告。レポートにそのまま載る")
	flag.Parse()

	if opts.out == "" {
		return flags{}, fmt.Errorf("%w: -out is required", errFlags)
	}

	return opts, nil
}

// run は評価セットを読み、計測し、レポートを書く。
func (s session) run(ctx context.Context) error {
	// 🔴 eval.LoadDataset は整合性の検査まで済ませて返す。dangling な正解キーを
	// 抱えたまま測ると、症状は「recall が低い」だけになり、原因が注釈だと分からない。
	data, err := loadDataset(s.opts)
	if err != nil {
		return err
	}

	embedder, err := s.buildEmbedder()
	if err != nil {
		return err
	}

	target, err := s.openEvalStore(ctx, embedder)
	if err != nil {
		return err
	}

	defer func() {
		if err := target.store.Close(); err != nil {
			s.log.Error("failed to close the store", slog.Any("error", err))
		}
	}()

	measurement, err := s.measure(ctx, target.store, embedder, data.Dataset)
	if err != nil {
		return err
	}

	environment, err := s.environment(ctx, target, embedder)
	if err != nil {
		return err
	}

	report := eval.NewReport(environment, data.Inputs, measurement, time.Now())
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

// openEvalStore は評価専用 DB を作り直し、移行済みで空のストアを返す。
//
// 🔴 開発用の recall にもテスト用の recall_test にも相乗りしない。無関係な行が
// 1件でも混ざれば、それは順位に入り込んで recall を汚染する。症状は
// 「recall が少し低い」だけなので気づけない (ADR 0013)。
func (s session) openEvalStore(ctx context.Context, embedder embed.Embedder) (evalStore, error) {
	if err := recreateEvalDatabase(ctx); err != nil {
		return evalStore{}, err
	}

	db, err := postgres.Open(ctx, evalDSN)
	if err != nil {
		return evalStore{}, fmt.Errorf("open evaluation database: %w", err)
	}

	store, err := postgres.New(db, embedder, bigram.New())
	if err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("build store: %w", err), db.Close())
	}

	if err := store.Migrate(ctx); err != nil {
		return evalStore{}, errors.Join(fmt.Errorf("migrate: %w", err), store.Close())
	}

	return evalStore{store: store, db: db}, nil
}

// recreateEvalDatabase は評価用 DB を落として作り直す。
//
// 作り直すのは、前回の残骸に依存した計測が「たまたま良い数字を出す」状態を
// 作らないため。internal/store/postgres/main_test.go の流儀を踏襲している。
func recreateEvalDatabase(ctx context.Context) error {
	// ドライバ名は pgx。postgres パッケージが登録するのと同じ値である。
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return fmt.Errorf("%w: open admin connection: %w", errEvalDatabase, err)
	}

	if err := resetDatabase(ctx, admin); err != nil {
		return errors.Join(err, admin.Close())
	}

	if err := admin.Close(); err != nil {
		return fmt.Errorf("%w: close admin connection: %w", errEvalDatabase, err)
	}

	return nil
}

// resetDatabase は DROP して CREATE する。
//
// DB 名は識別子なのでプレースホルダにできない。値は定数 evalDBName だけで、
// 外部入力を連結する経路は無い。
func resetDatabase(ctx context.Context, admin *sql.DB) error {
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: cannot reach postgres, run `docker compose up -d`: %w",
			errEvalDatabase, err)
	}

	// FORCE は残った接続を切ってから落とす（PostgreSQL 13 以降）。
	if _, err := admin.ExecContext(ctx,
		`DROP DATABASE IF EXISTS `+evalDBName+` WITH (FORCE)`); err != nil {
		return fmt.Errorf("%w: drop %s: %w", errEvalDatabase, evalDBName, err)
	}

	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+evalDBName); err != nil {
		return fmt.Errorf("%w: create %s: %w", errEvalDatabase, evalDBName, err)
	}

	return nil
}

// measure は計測ループを組み立てて走らせる。
func (s session) measure(
	ctx context.Context, store *postgres.Store, embedder embed.Embedder, ds eval.Dataset,
) (eval.Measurement, error) {
	runner, err := eval.NewRunner(eval.Dependencies{
		Writer:         store,
		Searcher:       store,
		VectorSearcher: store,
		EmbedQuery:     embedQuery(embedder),
	})
	if err != nil {
		return eval.Measurement{}, fmt.Errorf("build runner: %w", err)
	}

	orgID, err := org.NewID(s.opts.rawOrg)
	if err != nil {
		return eval.Measurement{}, fmt.Errorf("org id: %w", err)
	}

	measurement, err := runner.Measure(ctx, ds, eval.Options{
		OrgID:  orgID,
		Alpha:  s.alpha(),
		Limit:  s.opts.limit,
		Rounds: s.opts.rounds,
	})
	if err != nil {
		return eval.Measurement{}, fmt.Errorf("measure: %w", err)
	}

	return measurement, nil
}

// alpha は -alpha が未指定なら設定の既定値を使う。
//
// ⚠️ どちらの値にも根拠は無い（要件定義 Q-3）。この評価が決着させる対象である。
func (s session) alpha() float32 {
	if s.opts.alpha < 0 {
		return s.cfg.DefaultAlpha
	}

	return float32(s.opts.alpha)
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

	versions, err := readServerVersions(ctx, target.db)
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
		GPUNote:         s.opts.gpuNote,
	}, nil
}

// readServerVersions は PostgreSQL と pgvector の版を読む。
func readServerVersions(ctx context.Context, db *sql.DB) (serverVersions, error) {
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

	return serverVersions{postgres: pg, pgvector: vector}, nil
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
		slog.Float64("alpha", float64(report.Conditions.Alpha)),
		// micro は正解チャンク単位の内訳。クエリ単位のマクロ平均とは別物で、
		// 「どのチャンクが拾えていないか」を見るにはこちらが要る。
		slog.Float64("micro_recall", summary.MicroRecall.Value),
		// 名指しの長文 gold がどれだけ拾えたか。Q-1 の交絡要因の見張りである。
		slog.Float64("long_chunk_recall", summary.LongChunkRecall.Value),
		slog.Float64("p95_with_embedding_ms", summary.Latency.WithEmbedding.P95MS),
		slog.Float64("p95_without_embedding_ms", summary.Latency.WithoutEmbedding.P95MS),
		slog.String("model_digest", report.Environment.ModelDigest),
	)
}
