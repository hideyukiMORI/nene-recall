// Package config は環境変数から設定を読む。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Store は永続化バックエンドの種類。
type Store string

const (
	// StorePostgres は PostgreSQL + pgvector。既定 (ADR 0007)。
	StorePostgres Store = "postgres"
	// StoreSQLite は SQLite + Go 側の総当たり内積。比較実測用に残している (ADR 0007)。
	StoreSQLite Store = "sqlite"
)

// Config はサーバの設定。
type Config struct {
	// Addr は待ち受けアドレス。RECALL_ADDR、既定 ":8080"。
	Addr string

	// Store は永続化バックエンド。RECALL_STORE、既定 "postgres"。
	//
	// sqlite を残しているのは、同一データで全探索と pgvector を比較実測するため。
	// 比較そのものが成果物になる (ADR 0007)。
	Store Store
	// DatabaseURL は Store=postgres のときの接続文字列。RECALL_DATABASE_URL。
	DatabaseURL string
	// DBPath は Store=sqlite のときのファイルパス。RECALL_DB_PATH、既定 "recall.db"。
	DBPath string

	// Embedder は埋め込みプロバイダ名。RECALL_EMBEDDER、既定 "ollama"。
	//
	// 既定がローカル実行なのは、ローカル利用が要件だから (ADR 0008)。
	// voyage は任意経路であり、選んだときだけ API キーを要求する。
	Embedder string
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

	// DefaultAlpha は合成の既定の重み。RECALL_DEFAULT_ALPHA、既定 0.7。
	//
	// 0.7 に根拠は無い。ADR 0009 の評価セットで最適値を探すまでの暫定値である
	// (要件定義 Q-3)。
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
		Embedder:        env("RECALL_EMBEDDER", "ollama"),
		EmbedModel:      env("RECALL_EMBED_MODEL", "bge-m3"),
		EmbedDimensions: 1024,
		OllamaBaseURL:   env("RECALL_OLLAMA_URL", "http://localhost:11434"),
		VoyageAPIKey:    os.Getenv("VOYAGE_API_KEY"),
		DefaultAlpha:    0.7,
	}

	if v := os.Getenv("RECALL_EMBED_DIMENSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: RECALL_EMBED_DIMENSIONS must be a positive integer, got %q", v)
		}
		c.EmbedDimensions = n
	}

	if v := os.Getenv("RECALL_DEFAULT_ALPHA"); v != "" {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil || f < 0 || f > 1 {
			return Config{}, fmt.Errorf("config: RECALL_DEFAULT_ALPHA must be within [0,1], got %q", v)
		}
		c.DefaultAlpha = float32(f)
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	switch c.Store {
	case StorePostgres:
		if c.DatabaseURL == "" {
			return fmt.Errorf("config: RECALL_DATABASE_URL is required when RECALL_STORE=postgres")
		}
	case StoreSQLite:
		if c.DBPath == "" {
			return fmt.Errorf("config: RECALL_DB_PATH is required when RECALL_STORE=sqlite")
		}
	default:
		return fmt.Errorf("config: RECALL_STORE must be %q or %q, got %q", StorePostgres, StoreSQLite, c.Store)
	}

	switch c.Embedder {
	case "ollama":
		if c.OllamaBaseURL == "" {
			return fmt.Errorf("config: RECALL_OLLAMA_URL is required when RECALL_EMBEDDER=ollama")
		}
	case "voyage":
		// voyage は任意経路。選んだときだけ課金が発生するので、キーもここでだけ要求する。
		if c.VoyageAPIKey == "" {
			return fmt.Errorf("config: VOYAGE_API_KEY is required when RECALL_EMBEDDER=voyage")
		}
	default:
		return fmt.Errorf("config: RECALL_EMBEDDER must be \"ollama\" or \"voyage\", got %q", c.Embedder)
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
