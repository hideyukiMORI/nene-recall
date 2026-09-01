// Package config は環境変数から設定を読む。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config はサーバの設定。
type Config struct {
	// Addr は待ち受けアドレス。RECALL_ADDR、既定 ":8080"。
	Addr string
	// DBPath は SQLite ファイルのパス。RECALL_DB_PATH、既定 "recall.db"。
	DBPath string
	// Embedder は埋め込みプロバイダ名。RECALL_EMBEDDER、既定 "voyage"。
	Embedder string
	// EmbedModel は埋め込みモデル名。RECALL_EMBED_MODEL、既定 "voyage-4"。
	EmbedModel string
	// EmbedDimensions は埋め込みの次元数。RECALL_EMBED_DIMENSIONS、既定 1024。
	EmbedDimensions int
	// VoyageAPIKey は VOYAGE_API_KEY から読む。
	//
	// ログにもエラー応答にも出さないこと。String() を実装しないのは意図的で、
	// 構造体ごと %v で出力しても値が漏れる。ロギング時は個別フィールドを選ぶ。
	VoyageAPIKey string
	// DefaultAlpha は合成の既定の重み。RECALL_DEFAULT_ALPHA、既定 0.7。
	DefaultAlpha float32
}

// Load は環境変数から設定を組み立てる。
//
// org_id の既定値は**意図的に存在しない**。サーバ側にフォールバックを置くと
// テナント分離が静かに壊れるため (docs/adr/0003-org-id-is-mandatory.md)。
func Load() (Config, error) {
	c := Config{
		Addr:            env("RECALL_ADDR", ":8080"),
		DBPath:          env("RECALL_DB_PATH", "recall.db"),
		Embedder:        env("RECALL_EMBEDDER", "voyage"),
		EmbedModel:      env("RECALL_EMBED_MODEL", "voyage-4"),
		EmbedDimensions: 1024,
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

	if c.Embedder == "voyage" && c.VoyageAPIKey == "" {
		return Config{}, fmt.Errorf("config: VOYAGE_API_KEY is required when RECALL_EMBEDDER=voyage")
	}

	return c, nil
}

// EmbedderID は保存済みベクトルとの互換判定に使う識別子を返す。
func (c Config) EmbedderID() string {
	return fmt.Sprintf("%s:%d", c.EmbedModel, c.EmbedDimensions)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
