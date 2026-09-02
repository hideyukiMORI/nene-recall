package config_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/config"
)

// setMinimalEnv は Load が成功する最小の環境を作る。
//
// 各変数を明示的に置くのは、開発者のシェルに残った RECALL_* がテスト結果を
// 変えないようにするため。t.Setenv なのでテスト終了時に元へ戻る。
func setMinimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RECALL_ADDR", "")
	t.Setenv("RECALL_STORE", "")
	t.Setenv("RECALL_DATABASE_URL", "postgres://localhost/recall")
	t.Setenv("RECALL_DB_PATH", "")
	t.Setenv("RECALL_TOKENIZER", "")
	t.Setenv("RECALL_EMBEDDER", "")
	t.Setenv("RECALL_EMBED_MODEL", "")
	t.Setenv("RECALL_EMBED_DIMENSIONS", "")
	t.Setenv("RECALL_OLLAMA_URL", "")
	t.Setenv("RECALL_DEFAULT_ALPHA", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("RECALL_API_TOKEN", "")
}

// TestLoadReadsAPIToken は共有 Bearer トークンの読み取りを固定する。
//
// 🔴 未設定は空文字であって、既定のトークンではない
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 3)。
// 既定値のある共有秘密は、設定を忘れた全員が同じ鍵を使う状態になる。
// 空でも Load は成功する——認証なしが個人のローカル利用の既定だからである。
func TestLoadReadsAPIToken(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() が失敗した: %v", err)
	}

	if cfg.APIToken != "" {
		t.Errorf("🔴 未設定なのに APIToken = %q（既定のトークンを置いていないか）", cfg.APIToken)
	}

	t.Setenv("RECALL_API_TOKEN", "s3cret")

	withToken, err := config.Load()
	if err != nil {
		t.Fatalf("Load() が失敗した: %v", err)
	}

	if withToken.APIToken != "s3cret" {
		t.Errorf("APIToken = %q, want %q", withToken.APIToken, "s3cret")
	}
}

// TestLoadAppliesDocumentedDefaults は、既定値が文書どおりであることを固定する。
//
// 既定が「完全ローカル・費用0円」の構成であることは ADR 0008 の決定そのものなので、
// ここが静かに変わると製品の建前が崩れる。
func TestLoadAppliesDocumentedDefaults(t *testing.T) {
	setMinimalEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() が失敗した: %v", err)
	}

	if got, want := cfg.Addr, ":8080"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}

	if got, want := cfg.Store, config.StorePostgres; got != want {
		t.Errorf("Store = %q, want %q", got, want)
	}

	// 🔴 既定が bigram であること。kagome は比較対象であって既定ではなく、
	// 既定を移すのは実測を見て別の ADR を書いてからである (ADR 0018)。
	if got, want := cfg.Tokenizer, config.TokenizerBigram; got != want {
		t.Errorf("Tokenizer = %q, want %q（既定が比較対象に移っていないか）", got, want)
	}

	if got, want := cfg.EmbedProvider, config.EmbedProviderOllama; got != want {
		t.Errorf("EmbedProvider = %q, want %q（既定が外部 API になっていないか）", got, want)
	}

	if got, want := cfg.EmbedModel, "bge-m3"; got != want {
		t.Errorf("EmbedModel = %q, want %q", got, want)
	}

	if got, want := cfg.EmbedDimensions, 1024; got != want {
		t.Errorf("EmbedDimensions = %d, want %d", got, want)
	}

	if got, want := cfg.DefaultAlpha, float32(0.8); got != want {
		t.Errorf("DefaultAlpha = %v, want %v", got, want)
	}
}

// TestLoadRejectsInvalidValues は、壊れた値で起動できないことを確認する。
//
// errors.Is で sentinel を見るのは、メッセージ文字列に依存したテストを書かないため。
func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := map[string]struct {
		key   string
		value string
		want  error
	}{
		"次元が 0":         {"RECALL_EMBED_DIMENSIONS", "0", config.ErrInvalidValue},
		"次元が負":          {"RECALL_EMBED_DIMENSIONS", "-1", config.ErrInvalidValue},
		"次元が非数値":        {"RECALL_EMBED_DIMENSIONS", "many", config.ErrInvalidValue},
		"alpha が 1 超":   {"RECALL_DEFAULT_ALPHA", "1.5", config.ErrInvalidValue},
		"alpha が負":      {"RECALL_DEFAULT_ALPHA", "-0.1", config.ErrInvalidValue},
		"alpha が非数値":    {"RECALL_DEFAULT_ALPHA", "high", config.ErrInvalidValue},
		"未知の store":     {"RECALL_STORE", "qdrant", config.ErrUnknownOption},
		"未知の tokenizer": {"RECALL_TOKENIZER", "mecab", config.ErrUnknownOption},
		"未知の embedder":  {"RECALL_EMBEDDER", "openai", config.ErrUnknownOption},
		"voyage でキーが無い": {"RECALL_EMBEDDER", "voyage", config.ErrMissingRequired},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			setMinimalEnv(t)
			t.Setenv(tc.key, tc.value)

			_, err := config.Load()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Load() error = %v, want errors.Is(err, %v)", err, tc.want)
			}
		})
	}
}

// TestLoadRequiresDatabaseURL は、接続文字列が無いまま起動できないことを確認する。
//
// 起動できてしまうと、最初のリクエストまで設定漏れが発覚しない。
func TestLoadRequiresDatabaseURL(t *testing.T) {
	setMinimalEnv(t)
	t.Setenv("RECALL_DATABASE_URL", "")

	_, err := config.Load()
	if !errors.Is(err, config.ErrMissingRequired) {
		t.Fatalf("Load() error = %v, want errors.Is(err, ErrMissingRequired)", err)
	}
}

// baseConfig は検証テスト用の、全フィールドを明示した正常な設定を返す。
//
// 全フィールドを書くのは exhaustruct が要求するからだが、要求している理由は
// 「フィールドが増えたときにテストが黙って通り続ける」のを防ぐためである。
func baseConfig() config.Config {
	return config.Config{
		Addr:            ":8080",
		Store:           config.StorePostgres,
		DatabaseURL:     "postgres://localhost/recall",
		DBPath:          "recall.db",
		Tokenizer:       config.TokenizerBigram,
		EmbedProvider:   config.EmbedProviderOllama,
		EmbedModel:      "bge-m3",
		EmbedDimensions: 1024,
		OllamaBaseURL:   "http://localhost:11434",
		VoyageAPIKey:    "",
		APIToken:        "",
		DefaultAlpha:    0.8,
		SearchMode:      config.SearchModeExhaustive,
		CandidateK:      100,
		HNSWEfSearch:    40,
	}
}

// TestValidateRejectsBadCombinations は、設定の組み合わせが破綻したまま
// 起動してしまわないことを確認する。
//
// Load 経由では既定値に吸収されて到達できない分岐（DBPath や OllamaBaseURL が空）も
// ここで踏む。既定値を消したときに検証が効かなくなっていることに気づけるようにするため。
func TestValidateRejectsBadCombinations(t *testing.T) {
	tests := map[string]struct {
		mutate func(*config.Config)
		want   error
	}{
		"既定の組み合わせ":            {func(*config.Config) {}, nil},
		"postgres だが DSN が無い": {func(c *config.Config) { c.DatabaseURL = "" }, config.ErrMissingRequired},
		"sqlite に切替":          {func(c *config.Config) { c.Store = config.StoreSQLite }, nil},
		"sqlite だが DBPath が無い": {
			func(c *config.Config) { c.Store = config.StoreSQLite; c.DBPath = "" },
			config.ErrMissingRequired,
		},
		"未知の Store":  {func(c *config.Config) { c.Store = "qdrant" }, config.ErrUnknownOption},
		"kagome に切替": {func(c *config.Config) { c.Tokenizer = config.TokenizerKagome }, nil},
		"union に切替":  {func(c *config.Config) { c.Tokenizer = config.TokenizerUnion }, nil},
		"未知の Tokenizer": {
			func(c *config.Config) { c.Tokenizer = "mecab" },
			config.ErrUnknownOption,
		},
		"未知の Embedder": {func(c *config.Config) { c.EmbedProvider = "openai" }, config.ErrUnknownOption},
		"voyage だがキーが無い": {
			func(c *config.Config) { c.EmbedProvider = config.EmbedProviderVoyage },
			config.ErrMissingRequired,
		},
		"voyage でキーあり": {
			func(c *config.Config) { c.EmbedProvider = config.EmbedProviderVoyage; c.VoyageAPIKey = "k" },
			nil,
		},
		"ollama だが URL が無い": {func(c *config.Config) { c.OllamaBaseURL = "" }, config.ErrMissingRequired},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			tc.mutate(&cfg)

			err := config.ValidateForTest(cfg)
			if tc.want == nil && err != nil {
				t.Fatalf("エラーを期待していないが返った: %v", err)
			}

			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want errors.Is(err, %v)", err, tc.want)
			}
		})
	}
}

// TestEmbedderIDDoesNotLeakAPIKey は、識別子に秘匿情報が混ざらないことを確認する。
func TestEmbedderIDDoesNotLeakAPIKey(t *testing.T) {
	cfg := baseConfig()
	cfg.EmbedProvider = config.EmbedProviderVoyage
	cfg.VoyageAPIKey = "secret-key"

	if got, want := cfg.EmbedderID(), "bge-m3:1024"; got != want {
		t.Fatalf("EmbedderID() = %q, want %q", got, want)
	}
}

// TestLoadReadsTheTokenizer は RECALL_TOKENIZER が読まれることを確認する。
//
// 🔴 既定が bigram であること自体は TestLoadAppliesDocumentedDefaults が見る。
// ここで見るのは「選べること」で、選べなければ ADR 0018・ADR 0021 の比較実測
// そのものができない。既定でない選択肢を全部踏むのは、片方だけ配線が
// 抜けていても既定へ落ちずに起動が失敗する、という性質を保つためである。
func TestLoadReadsTheTokenizer(t *testing.T) {
	for _, want := range []config.Tokenizer{config.TokenizerKagome, config.TokenizerUnion} {
		t.Run(string(want), func(t *testing.T) {
			setMinimalEnv(t)
			t.Setenv("RECALL_TOKENIZER", string(want))

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() が失敗した: %v", err)
			}

			if got := cfg.Tokenizer; got != want {
				t.Errorf("Tokenizer = %q, want %q", got, want)
			}
		})
	}
}
