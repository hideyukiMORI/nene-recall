// Package config は環境変数から設定を読む。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// 設定の失敗は3種類しかない。呼び出し側が errors.Is で分岐できるよう sentinel を公開し、
// どの環境変数が悪いのかは %w で包んだメッセージ側に持たせる。
//
// 動的 error（fmt.Errorf だけで作る error）を禁止しているのは、メッセージ文字列で
// 分岐するコードが生まれるのを防ぐため (GO-005)。
var (
	// ErrMissingRequired は必須の環境変数が空だったことを表す。
	ErrMissingRequired = errors.New("config: required value is missing")
	// ErrInvalidValue は値の形式・範囲が不正だったことを表す。
	ErrInvalidValue = errors.New("config: value is invalid")
	// ErrUnknownOption は列挙にない選択肢が指定されたことを表す。
	ErrUnknownOption = errors.New("config: unknown option")
)

// Store は永続化バックエンドの種類。
type Store string

const (
	// StorePostgres は PostgreSQL + pgvector。既定 (ADR 0007)。
	StorePostgres Store = "postgres"
	// StoreSQLite は SQLite + Go 側の総当たり内積。比較実測用に**実装済み**
	// (ADR 0017)。既定を移すのは実測を見てからである (ADR 0007)。
	StoreSQLite Store = "sqlite"
)

// Tokenizer は語彙検索のテキスト分割器の種類。
//
// 🔴 既定は bigram のままにする。kagome は**比較対象**として入っており
// (ADR 0018)、既定を移すのは実測を見て別の ADR を書いてからである。
//
// 🔴 分割器を変えると保存済みの lexeme_text と噛み合わない。ストアが
// tokenizer_id の不一致をエラーにするので静かには壊れないが、切り替えたら
// 取り込み直しが要る（ADR 0005 と同じ性質）。
type Tokenizer string

const (
	// TokenizerBigram は文字 bigram。既定 (ADR 0014)。
	TokenizerBigram Tokenizer = "bigram"
	// TokenizerKagome は形態素解析 (kagome + IPA 辞書)。比較実測用 (ADR 0018)。
	TokenizerKagome Tokenizer = "kagome"
)

// EmbedProvider は埋め込みプロバイダの種類。
//
// string のままにしないのは、選択肢が閉じていることを型で示し、switch の網羅を
// exhaustive linter に見張らせるため (GO-002)。増やすときは ADR を1本立てる。
type EmbedProvider string

const (
	// EmbedProviderOllama はローカル実行。既定 (ADR 0008)。
	EmbedProviderOllama EmbedProvider = "ollama"
	// EmbedProviderVoyage は外部 API。任意経路であり、選んだときだけ課金が発生する。
	EmbedProviderVoyage EmbedProvider = "voyage"
)

// Config はサーバの設定。
type Config struct {
	// Addr は待ち受けアドレス。RECALL_ADDR、既定 ":8080"。
	Addr string

	// Store は永続化バックエンド。RECALL_STORE、既定 "postgres"。
	//
	// sqlite があるのは、同一データで Go 側の全探索と pgvector を比較実測する
	// ため。比較そのものが成果物になる (ADR 0007)。実装は ADR 0017。
	Store Store
	// DatabaseURL は Store=postgres のときの接続文字列。RECALL_DATABASE_URL。
	DatabaseURL string
	// DBPath は Store=sqlite のときのファイルパス。RECALL_DB_PATH、既定 "recall.db"。
	DBPath string

	// Tokenizer は語彙検索の分割器。RECALL_TOKENIZER、既定 "bigram"。
	//
	// kagome があるのは、bigram と形態素のどちらが良いか（要件定義 Q-2）を
	// 同一データで比較実測するため。比較そのものが成果物になる (ADR 0018)。
	Tokenizer Tokenizer

	// EmbedProvider は埋め込みプロバイダ。RECALL_EMBEDDER、既定 "ollama"。
	//
	// 既定がローカル実行なのは、ローカル利用が要件だから (ADR 0008)。
	// voyage は任意経路であり、選んだときだけ API キーを要求する。
	EmbedProvider EmbedProvider
	// EmbedModel は埋め込みモデル名。RECALL_EMBED_MODEL、既定 "bge-m3"。
	EmbedModel string
	// EmbedDimensions は埋め込みの次元数。RECALL_EMBED_DIMENSIONS、既定 1024。
	//
	// bge-m3 も voyage-4 も既定 1024 なので、両者の切替でスキーマが変わらない。
	EmbedDimensions int
	// OllamaBaseURL は Embedder=ollama のときの接続先。RECALL_OLLAMA_URL。
	//
	// 既定は localhost だが、想定構成では Ollama を Windows ネイティブで走らせるので
	// 実際にはホスト側のアドレスを明示指定することになる。WSL の CUDA ユーザ空間
	// ライブラリが未配置でも RTX 3090 をそのまま使えるのがその理由 (ADR 0008)。
	// WSL からホストを引くアドレスは再起動で変わるため、既定に頼らず .env に書くこと。
	OllamaBaseURL string
	// VoyageAPIKey は VOYAGE_API_KEY から読む。Embedder=voyage のときだけ要る。
	//
	// ログにもエラー応答にも出さないこと。String() を実装しないのは意図的で、
	// 構造体ごと %v で出力しても値が漏れる。ロギング時は個別フィールドを選ぶ。
	VoyageAPIKey string

	// DefaultAlpha は合成の既定の重み。RECALL_DEFAULT_ALPHA、既定 0.8。
	//
	// 0.8 は 2026-09-02 の評価セット・bge-m3:1024・bigram・語彙スコアのクエリ内
	// 正規化という条件で、プラトー (0.7〜0.9) の中心として選んだ値である
	// (ADR 0015)。argmax ではなくプラトーの中心を採るのは、58クエリの指標が
	// 1クエリ = 0.017 揺れるため、最大点はゆらぎに引きずられるからである。
	//
	// 🔴 「最適値」ではない。正規化方式・分割器 (Tokenizer.ID)・埋め込みモデル
	// (Embedder.ID)・候補集合の作り方のどれかを変えたら測り直すこと
	// (ADR 0015 Decision 3)。
	DefaultAlpha float32
}

// Load は環境変数から設定を組み立てる。
//
// org_id の既定値は**意図的に存在しない**。サーバ側にフォールバックを置くと
// テナント分離が静かに壊れるため (docs/adr/0003-org-id-is-mandatory.md)。
func Load() (Config, error) {
	c := Config{
		Addr:            env("RECALL_ADDR", ":8080"),
		Store:           Store(env("RECALL_STORE", string(StorePostgres))),
		DatabaseURL:     os.Getenv("RECALL_DATABASE_URL"),
		DBPath:          env("RECALL_DB_PATH", "recall.db"),
		Tokenizer:       Tokenizer(env("RECALL_TOKENIZER", string(TokenizerBigram))),
		EmbedProvider:   EmbedProvider(env("RECALL_EMBEDDER", string(EmbedProviderOllama))),
		EmbedModel:      env("RECALL_EMBED_MODEL", "bge-m3"),
		EmbedDimensions: 1024,
		OllamaBaseURL:   env("RECALL_OLLAMA_URL", "http://localhost:11434"),
		VoyageAPIKey:    os.Getenv("VOYAGE_API_KEY"),
		DefaultAlpha:    0.8,
	}

	if v := os.Getenv("RECALL_EMBED_DIMENSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%w: RECALL_EMBED_DIMENSIONS must be a positive integer, got %q", ErrInvalidValue, v)
		}
		c.EmbedDimensions = n
	}

	if v := os.Getenv("RECALL_DEFAULT_ALPHA"); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil || f < 0 || f > 1 {
			return Config{}, fmt.Errorf("%w: RECALL_DEFAULT_ALPHA must be within [0,1], got %q", ErrInvalidValue, v)
		}
		c.DefaultAlpha = float32(f)
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if err := c.validateStore(); err != nil {
		return err
	}

	if err := c.validateTokenizer(); err != nil {
		return err
	}

	return c.validateEmbedder()
}

// validateTokenizer は分割器の選択を検証する。
//
// 🔴 未知の値を既定へ黙って倒さない。綴り誤りが bigram として起動すると、
// 「kagome で測ったつもりの数字」が bigram のものになる。設定の誤りは
// 設定を読んだ直後に落とす。
func (c Config) validateTokenizer() error {
	switch c.Tokenizer {
	case TokenizerBigram, TokenizerKagome:
		return nil
	default:
		return fmt.Errorf("%w: RECALL_TOKENIZER must be %q or %q, got %q",
			ErrUnknownOption, TokenizerBigram, TokenizerKagome, c.Tokenizer)
	}
}

// validateStore は永続化バックエンドの選択と、その選択が要求する値の有無を検証する。
func (c Config) validateStore() error {
	switch c.Store {
	case StorePostgres:
		if c.DatabaseURL == "" {
			return fmt.Errorf("%w: RECALL_DATABASE_URL is required when RECALL_STORE=postgres", ErrMissingRequired)
		}
	case StoreSQLite:
		if c.DBPath == "" {
			return fmt.Errorf("%w: RECALL_DB_PATH is required when RECALL_STORE=sqlite", ErrMissingRequired)
		}
	default:
		return fmt.Errorf("%w: RECALL_STORE must be %q or %q, got %q",
			ErrUnknownOption, StorePostgres, StoreSQLite, c.Store)
	}

	return nil
}

// validateEmbedder は埋め込みプロバイダの選択と、その選択が要求する値の有無を検証する。
//
// API キーを voyage のときだけ要求するのは、既定構成（ローカル）に外部サービスの
// 前提を持ち込まないため (ADR 0008)。
func (c Config) validateEmbedder() error {
	switch c.EmbedProvider {
	case EmbedProviderOllama:
		if c.OllamaBaseURL == "" {
			return fmt.Errorf("%w: RECALL_OLLAMA_URL is required when RECALL_EMBEDDER=ollama", ErrMissingRequired)
		}
	case EmbedProviderVoyage:
		if c.VoyageAPIKey == "" {
			return fmt.Errorf("%w: VOYAGE_API_KEY is required when RECALL_EMBEDDER=voyage", ErrMissingRequired)
		}
	default:
		return fmt.Errorf("%w: RECALL_EMBEDDER must be %q or %q, got %q",
			ErrUnknownOption, EmbedProviderOllama, EmbedProviderVoyage, c.EmbedProvider)
	}

	return nil
}

// EmbedderID は保存済みベクトルとの互換判定に使う識別子を返す。
//
// モデルを変えると、次元が同じでもベクトル空間の互換性が失われる。
// 検知しないと「エラーにならないまま無意味なスコアが返る」(ADR 0005)。
func (c Config) EmbedderID() string {
	return fmt.Sprintf("%s:%d", c.EmbedModel, c.EmbedDimensions)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
