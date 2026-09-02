// Command recall は NeNe Recall の HTTP サーバを起動する。
//
// ここが唯一の配線点である。具体ストア (internal/store/postgres) と
// 具体 Embedder (internal/embed/ollama) を import してよいのはこのパッケージだけで、
// depguard がそれを強制する (ARC-001)。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
	"github.com/hideyukiMORI/nene-recall/internal/httpapi"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// ollamaTimeout は埋め込み1リクエストの上限。
//
// 根拠: コールドスタート（モデルのロードを含む初回）が実測 18.4 秒、
// ウォーム時の 32本バッチが 0.365 秒 (docs/benchmarks/2026-09-01-baseline.md)。
// 初回のロードを跨いでも間に合い、かつ無期限にはならない値として 60 秒を置く。
//
// 🔴 環境変数にしない。値を変える実測上の理由が無く、増やした設定は必ず誰かが
// 誤って触る。変える必要が出たらここを直してコミットする（判断が履歴に残る）。
const ollamaTimeout = 60 * time.Second

// migrateTimeout は起動時のマイグレーションの上限。
const migrateTimeout = 30 * time.Second

// shutdownTimeout は graceful shutdown の上限。
const shutdownTimeout = 10 * time.Second

// errVoyageNotImplemented は Voyage 経路が未実装であることを表す。
var errVoyageNotImplemented = errors.New("recall: the voyage embedder is not implemented yet")

// errUnknownStore は設定が未知のストアを指していることを表す。
//
// config.validateStore が既に弾いているので、通常はここへ来ない。番人が
// 無いと「どのストアも組み立てられなかった」が nil のストアとして先へ進む。
var errUnknownStore = errors.New("recall: unknown store")

// errUnknownTokenizer は設定が未知の分割器を指していることを表す。
//
// config.validateTokenizer が既に弾いているので、通常はここへ来ない。
// errUnknownStore と同じく、番人が無いと nil の分割器で先へ進む。
var errUnknownTokenizer = errors.New("recall: unknown tokenizer")

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(log); err != nil {
		log.Error("startup failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// embedderBundle は埋め込みプロバイダと、その疎通確認をまとめたもの。
//
// Ping を embed.Embedder の契約に足していないので（到達性の確認は変換の契約では
// ない）、配線点で両者を束ねて持ち回る。
type embedderBundle struct {
	embedder embed.Embedder
	ping     func(ctx context.Context) error
}

// run はサーバを組み立てて動かす。
//
// 🔴 DB へ到達できなければ起動に失敗するが、Ollama へ到達できなくても起動する。
// この非対称は意図的である。DB は起動時のマイグレーションに必須で、無ければ
// 何も始まらない。一方 Ollama は Windows 側で別のライフサイクルを持っており、
// 起動時に疎通を強制すると「Ollama を先に上げないと Recall が上がらない」という
// 順序依存が生まれる。Ollama の不調は /readyz の 503 と検索時の 503 で表面化させる。
func run(log *slog.Logger) error {
	// 🔴 cfg を構造体ごとログに出さないこと。config.Config は String() を実装して
	// いないので、%v や slog.Any に渡すと VoyageAPIKey がそのまま出る。
	// 出してよいフィールドを1つずつ選ぶ (GO-014)。
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	bundle, err := buildEmbedder(cfg)
	if err != nil {
		return err
	}

	tokenizer, err := buildTokenizer(cfg)
	if err != nil {
		return err
	}

	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancelMigrate()

	store, err := buildStore(migrateCtx, cfg, bundle.embedder, tokenizer)
	if err != nil {
		return err
	}

	// 🔴 store を閉じるのはサーバが止まった後になる。serve が返るのは
	// srv.Shutdown が終わってからで、この defer はそのさらに後に走る。
	// 順序が逆だと、まだ処理中の要求が閉じた接続プールを掴む。
	defer func() {
		if err := store.Close(); err != nil {
			log.Error("failed to close the store", slog.Any("error", err))
		}
	}()

	handler, err := buildHandler(cfg, log, store, bundle)
	if err != nil {
		return err
	}

	log.Info("starting",
		slog.String("addr", cfg.Addr),
		slog.String("store", string(cfg.Store)),
		slog.String("embedder_id", cfg.EmbedderID()),
		// 🔴 分割器の識別子を出す。取り込み済みのデータがどの規則で分割された
		// ものかは tokenizer_id が決めており、切り替えたことに気づかないまま
		// 起動すると最初の検索が不一致エラーで落ちる（ADR 0018）。
		slog.String("tokenizer_id", tokenizer.ID()),
		slog.String("ollama_url", cfg.OllamaBaseURL),
		// 🔴 トークンそのものは絶対に出さない。長さも先頭数文字も出さない。
		// 運用者が知る必要があるのは「認証が効いているか」だけである
		// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 3)。
		slog.String("auth", authState(cfg)),
	)

	return serve(log, cfg, handler)
}

// authState は起動ログに出す認証の状態を返す。
//
// 🔴 戻り値は "enabled" か "disabled" の2値だけである。トークンの一部・長さ・
// ハッシュのいずれもここから出さない。RECALL_API_TOKEN は VOYAGE_API_KEY と
// 同じ扱いの秘密であり、config.Config が String() を持たないのと同じ理由で、
// 値がログへ流れる経路を1本も作らない (GO-014)。
func authState(cfg config.Config) string {
	if cfg.APIToken == "" {
		return "disabled"
	}

	return "enabled"
}

// buildEmbedder は設定から埋め込みプロバイダを組み立てる。
func buildEmbedder(cfg config.Config) (embedderBundle, error) {
	switch cfg.EmbedProvider {
	case config.EmbedProviderOllama:
		client, err := ollama.New(ollama.Config{
			BaseURL:    cfg.OllamaBaseURL,
			Model:      cfg.EmbedModel,
			Dimensions: cfg.EmbedDimensions,
			BatchSize:  ollama.DefaultBatchSize,
			HTTPClient: &http.Client{
				Transport:     nil, // 既定の Transport を使う
				CheckRedirect: nil, // 既定のリダイレクト方針
				Jar:           nil, // Cookie を持たない
				Timeout:       ollamaTimeout,
			},
		})
		if err != nil {
			return embedderBundle{}, fmt.Errorf("build ollama embedder: %w", err)
		}

		return embedderBundle{embedder: client, ping: client.Ping}, nil

	case config.EmbedProviderVoyage:
		// 設定としては valid だが起動はしない。Phase 1 の状態として正しい。
		return embedderBundle{}, fmt.Errorf(
			"%w: set RECALL_EMBEDDER=ollama (the default) to use the local provider",
			errVoyageNotImplemented)
	}

	// config.validate が未知の値を既に拒否しているので、ここへは来ない。
	return embedderBundle{}, fmt.Errorf("%w: %q", errVoyageNotImplemented, cfg.EmbedProvider)
}

// buildTokenizer は設定から語彙分割器を組み立てる。
//
// 🔴 既定は bigram である。kagome は比較実測のために選べる経路であって、
// 既定を移すのは実測を見て ADR を書いてからである (ADR 0018)。
//
// ⚠️ kagome.New だけが error を返す。辞書の読み込みを含むためで、契約
// (lexical.Tokenizer) の違いではない。この非対称はこの関数1つに閉じる。
func buildTokenizer(cfg config.Config) (lexical.Tokenizer, error) {
	switch cfg.Tokenizer {
	case config.TokenizerBigram:
		return bigram.New(), nil
	case config.TokenizerKagome:
		morphological, err := kagome.New()
		if err != nil {
			return nil, fmt.Errorf("build kagome tokenizer: %w", err)
		}

		return morphological, nil
	}

	// config.validate が未知の値を既に拒否しているので、ここへは来ない。
	return nil, fmt.Errorf("%w: %q", errUnknownTokenizer, cfg.Tokenizer)
}

// backingStore は配線点が扱うストアの口。
//
// 🔴 internal/index に足さない。Ping と Close は「検索する」「書き込む」という
// 契約の一部ではなく、プロセスのライフサイクルの都合である。契約に混ぜると、
// すべてのストア実装が配線点の都合に付き合わされる（SearchVector を
// index.Searcher に足さなかったのと同じ判断軸・ADR 0013）。
//
// 🔑 2つの具体ストア (postgres / sqlite) が同じ形をしていることを、ここで
// コンパイル時に確かめている。片方に生えたメソッドがもう片方に無ければ、
// 配線が通らない。
type backingStore interface {
	index.Searcher
	index.Writer

	// Ping は /readyz の Probe が呼ぶ。
	Ping(ctx context.Context) error
	// Close はサーバが止まったあとに呼ぶ。
	Close() error
}

// buildStore は設定からストアを組み立て、マイグレーションまで済ませる。
//
// 🔴 既定は postgres である。sqlite は比較実測のために選べる経路であって、
// 既定を移すのは実測を見て ADR を書いてからである (ADR 0007 / ADR 0017)。
func buildStore(
	ctx context.Context, cfg config.Config, embedder embed.Embedder, tokenizer lexical.Tokenizer,
) (backingStore, error) {
	switch cfg.Store {
	case config.StorePostgres:
		return openPostgres(ctx, cfg, embedder, tokenizer)
	case config.StoreSQLite:
		return openSQLite(ctx, cfg, embedder, tokenizer)
	}

	// config.validate が未知の値を既に拒否しているので、ここへは来ない。
	return nil, fmt.Errorf("%w: %q", errUnknownStore, cfg.Store)
}

// openPostgres は接続を開き、次元の突き合わせとマイグレーションを行う。
func openPostgres(
	ctx context.Context, cfg config.Config, embedder embed.Embedder, tokenizer lexical.Tokenizer,
) (backingStore, error) {
	db, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// ここで Embedder の次元と vector(1024) 列の突き合わせが効く。
	// 食い違っていれば「起動はするが取り込みが全部落ちる」前に落ちる。
	// 🔴 融合方式を配線点で明示する。ゼロ値でも同じ方式になるが、書かないと
	// 「既定がどちらか」がコードから読めなくなる。加重和は要件定義 F-4 と
	// OpenAPI が定める契約そのものなので、サーバはこれを使う。方式を変えるのは
	// 実測を見て ADR を書いてからである。
	store, err := postgres.New(db, embedder, tokenizer, postgres.FusionWeightedSum)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build store: %w", err), db.Close())
	}

	if err := store.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate: %w", err), store.Close())
	}

	return store, nil
}

// openSQLite はファイルを開き、次元の突き合わせとマイグレーションを行う。
//
// 🔑 postgres 側と手順を1つも変えていない。比較実測は「同じ手順で組み立てた
// 2つのストアを、同じデータで測る」ことが前提であり、配線に差があると
// その差が結果に混ざる (ADR 0017)。
//
// 🔴 融合方式を引数に取らない。sqlite 側は加重和しか実装していないので、
// 選べるかのような口を配線点に開けない (ADR 0017 Decision 4)。
func openSQLite(
	ctx context.Context, cfg config.Config, embedder embed.Embedder, tokenizer lexical.Tokenizer,
) (backingStore, error) {
	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store, err := sqlite.New(db, embedder, tokenizer)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build store: %w", err), db.Close())
	}

	if err := store.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate: %w", err), store.Close())
	}

	return store, nil
}

// buildHandler は HTTP ハンドラを組み立てる。
func buildHandler(
	cfg config.Config, log *slog.Logger, store backingStore, bundle embedderBundle,
) (http.Handler, error) {
	srv, err := httpapi.New(cfg, log, httpapi.Dependencies{
		Searcher:   store,
		Writer:     store,
		EmbedderID: cfg.EmbedderID(),
		Probes: []httpapi.Probe{
			{Name: "database", Check: store.Ping},
			{Name: "embedder", Check: bundle.ping},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build http server: %w", err)
	}

	return srv.Routes(), nil
}

// serve は待ち受けを開始し、シグナルで graceful に止める。
func serve(log *slog.Logger, cfg config.Config, handler http.Handler) error {
	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// タイムアウトは明示する。既定値は「無制限」であり、
		// 遅い読み書きで接続を占有される（slowloris）経路がそのまま残る。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// 🔴 WriteTimeout は 60 秒のまま。取り込みは埋め込みの生成を含むので、
		// 1リクエストあたりのチャンク数を運用側で抑える前提になる。
		// 実測 87.8 件/秒 から、1,000 チャンクで約 11 秒。10万件なら 100 リクエストで
		// 合計約 18 分となり、ベンチの見積り (docs/benchmarks/2026-09-01-baseline.md) と
		// 一致する。1リクエストを大きくしすぎるとここで切れる。
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	select {
	case err := <-listenErr:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	return nil
}
