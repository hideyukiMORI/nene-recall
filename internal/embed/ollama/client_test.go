package ollama_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
)

// TestEmbedSplitsIntoBatches は分割・順序・結合を一度に見る。
//
// 🔑 分割は性能のためだけの実装詳細ではない。実測で 1本ずつ 11.8 件/秒 に対し
// 32本で 87.8 件/秒（8倍）であり、10万件で 2時間21分 と 18分 の差になる。
// 一方で分割は「同順・同数」という契約を壊しうる唯一の場所でもある。
func TestEmbedSplitsIntoBatches(t *testing.T) {
	server := newRecordingOllama()
	client := startFakeOllama(t, server, 32)

	texts := numberedTexts(70)

	got, err := client.Embed(t.Context(), texts, embed.KindDocument)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// 70 = 32 + 32 + 6。バッチ境界がずれると件数か本数が合わなくなる。
	wantSizes := []int{32, 32, 6}
	if len(server.batches) != len(wantSizes) {
		t.Fatalf("リクエスト数 = %d, want %d", len(server.batches), len(wantSizes))
	}

	for i, want := range wantSizes {
		if len(server.batches[i]) != want {
			t.Errorf("リクエスト %d の本数 = %d, want %d", i, len(server.batches[i]), want)
		}
	}

	// 33本目（添字32）は2回目のリクエストの先頭に来る。境界の off-by-one を見る。
	if first := server.batches[1][0]; first != "t32" {
		t.Errorf("2回目の先頭 = %q, want %q", first, "t32")
	}

	if len(got) != len(texts) {
		t.Fatalf("返り値の本数 = %d, want %d", len(got), len(texts))
	}

	for i, v := range got {
		if n := decodeIndex(t, v); n != i+1 {
			t.Errorf("返り値 %d の順序が違う: 復元した番号 = %d, want %d", i, n, i+1)
		}
	}
}

// TestEmbedNormalizesVectors は契約（長さ1）を実装側で必ず満たすことを見る。
func TestEmbedNormalizesVectors(t *testing.T) {
	server := newRecordingOllama()
	server.respond = func(inputs []string) [][]float32 {
		out := make([][]float32, 0, len(inputs))
		for range inputs {
			// ノルム 5 のベクトル。正規化しなければ 5 のまま返る。
			out = append(out, []float32{3, 4, 0, 0})
		}

		return out
	}

	client := startFakeOllama(t, server, 32)

	got, err := client.Embed(t.Context(), []string{"a"}, embed.KindDocument)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	var sum float64
	for _, x := range got[0] {
		sum += float64(x) * float64(x)
	}

	if math.Abs(sum-1.0) > 1e-6 {
		t.Errorf("二乗和 = %v, want 1.0（正規化されていない）", sum)
	}
}

// TestEmbedRejectsBrokenResponses は応答の破損を不可用として扱うことを見る。
//
// 🔴 いずれも「少ないぶんだけ返す」「0 で埋める」といった救済をしない。
// 救済すると、呼び出し側から見て正常な応答と区別がつかなくなる。
func TestEmbedRejectsBrokenResponses(t *testing.T) {
	cases := map[string]func(inputs []string) [][]float32{
		"本数が足りない": func(_ []string) [][]float32 {
			return [][]float32{{1, 0, 0, 0}}
		},
		"次元が違う": func(inputs []string) [][]float32 {
			out := make([][]float32, 0, len(inputs))
			for range inputs {
				out = append(out, []float32{1, 0})
			}

			return out
		},
		"ゼロベクトル": func(inputs []string) [][]float32 {
			out := make([][]float32, 0, len(inputs))
			for range inputs {
				out = append(out, []float32{0, 0, 0, 0})
			}

			return out
		},
	}

	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			server := newRecordingOllama()
			server.respond = respond
			client := startFakeOllama(t, server, 32)

			_, err := client.Embed(t.Context(), []string{"a", "b"}, embed.KindDocument)
			if !errors.Is(err, embed.ErrProviderUnavailable) {
				t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
			}
		})
	}
}

// TestEmbedRejectsUnknownKind は未知の Kind を既定に倒さないことを見る。
//
// 既定に倒すと、接頭辞の要るモデルで品質だけが落ちて症状が出ない（ADR 0008）。
func TestEmbedRejectsUnknownKind(t *testing.T) {
	client := startFakeOllama(t, newRecordingOllama(), 32)

	_, err := client.Embed(t.Context(), []string{"a"}, embed.Kind("passage"))
	if !errors.Is(err, embed.ErrUnsupportedKind) {
		t.Fatalf("err = %v, want embed.ErrUnsupportedKind", err)
	}
}

// TestEmbedAcceptsBothKinds は取り込みと検索の双方の Kind を受けることを見る。
func TestEmbedAcceptsBothKinds(t *testing.T) {
	client := startFakeOllama(t, newRecordingOllama(), 32)

	for _, kind := range []embed.Kind{embed.KindDocument, embed.KindQuery} {
		if _, err := client.Embed(t.Context(), []string{"a"}, kind); err != nil {
			t.Errorf("Kind %s: %v", kind, err)
		}
	}
}

// TestEmbedReportsPullHintOn404 は運用者が次の一手を読めることを見る。
//
// モデル未取得はローカル実行で最も起きやすい失敗で、対処は ollama pull だけである。
// メッセージにそれが無いと、原因は分かっても直し方が分からない。
func TestEmbedReportsPullHintOn404(t *testing.T) {
	server := newRecordingOllama()
	server.status = http.StatusNotFound
	server.rawBody = `{"error":"model 'fake-model' not found"}`
	client := startFakeOllama(t, server, 32)

	_, err := client.Embed(t.Context(), []string{"a"}, embed.KindDocument)
	if !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want embed.ErrProviderUnavailable", err)
	}

	if !strings.Contains(err.Error(), "ollama pull fake-model") {
		t.Errorf("メッセージに次の一手が無い: %v", err)
	}
}

// TestEmbedReportsNon200 はその他の非 200 も不可用として扱うことを見る。
func TestEmbedReportsNon200(t *testing.T) {
	server := newRecordingOllama()
	server.status = http.StatusInternalServerError
	server.rawBody = `{"error":"boom"}`
	client := startFakeOllama(t, server, 32)

	_, err := client.Embed(t.Context(), []string{"a"}, embed.KindDocument)
	if !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestEmbedFailsWhenUnreachable は接続不能を不可用として扱うことを見る。
//
// リトライしないので、Ollama が落ちていれば即座にここへ来る。
func TestEmbedFailsWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(newRecordingOllama())
	url := srv.URL
	httpClient := srv.Client()
	srv.Close() // 閉じたアドレスへ向ける

	client := newClient(t, url, 32, httpClient)

	_, err := client.Embed(t.Context(), []string{"a"}, embed.KindDocument)
	if !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestEmbedHonorsContextCancellation は ctx の打ち切りが伝わることを見る。
//
// 元の error を連鎖に残しているので、呼び出し側は不可用と打ち切りを区別できる。
func TestEmbedHonorsContextCancellation(t *testing.T) {
	client := startFakeOllama(t, newRecordingOllama(), 32)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.Embed(ctx, []string{"a"}, embed.KindDocument)
	if !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want embed.ErrProviderUnavailable", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("context.Canceled が連鎖に残っていない: %v", err)
	}
}

// TestEmbedWithNoTextsMakesNoRequest は空入力で往復しないことを見る。
func TestEmbedWithNoTextsMakesNoRequest(t *testing.T) {
	server := newRecordingOllama()
	client := startFakeOllama(t, server, 32)

	got, err := client.Embed(t.Context(), nil, embed.KindDocument)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("返り値 = %d 件, want 0", len(got))
	}

	if len(server.batches) != 0 {
		t.Errorf("リクエストを %d 回出した, want 0", len(server.batches))
	}
}

// TestNewValidatesConfig は配線の取りこぼしを構築時に落とすことを見る。
//
// ゼロ値のまま進むと「起動はするが埋め込みが毎回失敗する」状態になり、
// 原因が設定だと気づきにくい。
func TestNewValidatesConfig(t *testing.T) {
	valid := ollama.Config{
		BaseURL:    "http://localhost:11434",
		Model:      "bge-m3",
		Dimensions: 1024,
		BatchSize:  ollama.DefaultBatchSize,
		HTTPClient: stubHTTPClient(),
	}

	cases := map[string]func(c *ollama.Config){
		"BaseURL が空":       func(c *ollama.Config) { c.BaseURL = "" },
		"Model が空":         func(c *ollama.Config) { c.Model = "" },
		"Dimensions が 0":   func(c *ollama.Config) { c.Dimensions = 0 },
		"BatchSize が 0":    func(c *ollama.Config) { c.BatchSize = 0 },
		"HTTPClient が nil": func(c *ollama.Config) { c.HTTPClient = nil },
	}

	if _, err := ollama.New(valid); err != nil {
		t.Fatalf("正しい Config が拒否された: %v", err)
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			broken := valid
			breakIt(&broken)

			if _, err := ollama.New(broken); err == nil {
				t.Errorf("%s が受け入れられた", name)
			}
		})
	}
}

// TestIDMatchesConfigEmbedderID は識別子の書式を config 側と揃える。
//
// 🔴 ここがずれると、保存済みベクトルの embedder_id と現在の設定が実質同じでも
// 不一致と判定され、取り込みも検索も 503 になる。書式は2箇所にあるので、
// 片方を直したときにもう片方が追随していないことを機械で気づけるようにする。
func TestIDMatchesConfigEmbedderID(t *testing.T) {
	t.Setenv("RECALL_DATABASE_URL", "postgres://recall:recall@localhost:5433/recall?sslmode=disable")
	t.Setenv("RECALL_EMBED_MODEL", "bge-m3")
	t.Setenv("RECALL_EMBED_DIMENSIONS", "1024")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	fromClient, err := ollama.New(ollama.Config{
		BaseURL:    "http://localhost:11434",
		Model:      cfg.EmbedModel,
		Dimensions: cfg.EmbedDimensions,
		BatchSize:  ollama.DefaultBatchSize,
		HTTPClient: stubHTTPClient(),
	})
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}

	if fromClient.ID() != cfg.EmbedderID() {
		t.Errorf("Client.ID() = %q, config.EmbedderID() = %q（書式がずれている）",
			fromClient.ID(), cfg.EmbedderID())
	}

	if want := "bge-m3:1024"; fromClient.ID() != want {
		t.Errorf("Client.ID() = %q, want %q", fromClient.ID(), want)
	}
}

// TestPingUsesShowEndpoint は疎通確認が埋め込みを誘発しないことを見る。
//
// 🔴 /api/embed で疎通を見るとモデルのロード（コールドスタート実測 18.4 秒）を
// 誘発する。ヘルスチェックのたびに GPU を占有することになるので /api/show を使う。
func TestPingUsesShowEndpoint(t *testing.T) {
	server := newRecordingOllama()
	client := startFakeOllama(t, server, 32)

	if err := client.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if server.shows != 1 {
		t.Errorf("/api/show の呼び出し回数 = %d, want 1", server.shows)
	}

	if len(server.batches) != 0 {
		t.Errorf("Ping が /api/embed を呼んだ（%d 回）。コールドスタートを誘発する", len(server.batches))
	}
}

// TestPingReportsUnavailable はモデル未取得を不可用として報告することを見る。
func TestPingReportsUnavailable(t *testing.T) {
	server := newRecordingOllama()
	server.status = http.StatusNotFound
	server.rawBody = `{"error":"model not found"}`
	client := startFakeOllama(t, server, 32)

	if err := client.Ping(t.Context()); !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestDimensionsReportsConfiguredValue は次元数がそのまま返ることを見る。
func TestDimensionsReportsConfiguredValue(t *testing.T) {
	client := startFakeOllama(t, newRecordingOllama(), 32)

	if got := client.Dimensions(); got != testDimensions {
		t.Errorf("Dimensions() = %d, want %d", got, testDimensions)
	}
}
