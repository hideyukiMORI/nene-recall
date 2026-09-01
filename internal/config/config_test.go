package config

import "testing"

// TestValidateRejectsBadCombinations は、設定の組み合わせが破綻したまま
// 起動してしまわないことを確認する。
//
// 特に「Store=postgres なのに接続文字列が無い」と「Embedder=voyage なのに
// API キーが無い」は、起動できてしまうと最初のリクエストまで発覚しない。
func TestValidateRejectsBadCombinations(t *testing.T) {
	base := func() Config {
		return Config{
			Store:           StorePostgres,
			DatabaseURL:     "postgres://localhost/recall",
			Embedder:        "ollama",
			OllamaBaseURL:   "http://localhost:11434",
			EmbedModel:      "bge-m3",
			EmbedDimensions: 1024,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"既定の組み合わせ", func(*Config) {}, false},
		{"postgres だが DSN が無い", func(c *Config) { c.DatabaseURL = "" }, true},
		{"sqlite に切替（DBPath あり）", func(c *Config) { c.Store = StoreSQLite; c.DBPath = "recall.db" }, false},
		{"sqlite だが DBPath が無い", func(c *Config) { c.Store = StoreSQLite; c.DBPath = "" }, true},
		{"未知の Store", func(c *Config) { c.Store = "qdrant" }, true},
		{"voyage だがキーが無い", func(c *Config) { c.Embedder = "voyage" }, true},
		{"voyage でキーあり", func(c *Config) { c.Embedder = "voyage"; c.VoyageAPIKey = "k" }, false},
		{"ollama だが URL が無い", func(c *Config) { c.OllamaBaseURL = "" }, true},
		{"未知の Embedder", func(c *Config) { c.Embedder = "openai" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			err := c.validate()
			if tt.wantErr && err == nil {
				t.Fatal("エラーを期待したが nil だった")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("エラーを期待していないが返った: %v", err)
			}
		})
	}
}

// TestEmbedderIDDoesNotLeakAPIKey は、識別子に秘匿情報が混ざらないことを確認する。
func TestEmbedderIDDoesNotLeakAPIKey(t *testing.T) {
	c := Config{EmbedModel: "bge-m3", EmbedDimensions: 1024, VoyageAPIKey: "secret-key"}
	if got, want := c.EmbedderID(), "bge-m3:1024"; got != want {
		t.Fatalf("EmbedderID() = %q, want %q", got, want)
	}
}
