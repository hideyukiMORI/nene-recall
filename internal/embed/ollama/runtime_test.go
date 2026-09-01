package ollama_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
)

// TestRuntimeReadsTheVersionAndTheModelDigest は、レポートに載せる素性が
// 取れることを見る。
//
// 🔑 これは評価レポートの再現性のためにある。Embedder.ID() は "bge-m3:1024" で
// しかなく、digest もランタイム版も区別しない。同じタグで別の重みが引かれても
// ADR 0005 の不一致検知は発火しない（ベンチの追記が残リスクとして記録している）。
func TestRuntimeReadsTheVersionAndTheModelDigest(t *testing.T) {
	server := newRecordingOllama()
	client := startFakeOllama(t, server, 32)

	got, err := client.Runtime(t.Context())
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	if got.Version != "0.33.2" {
		t.Errorf("Version = %q, want %q", got.Version, "0.33.2")
	}

	if want := strings.Repeat("b", 64); got.Digest != want {
		t.Errorf("Digest = %q, want %q", got.Digest, want)
	}
}

// TestRuntimeResolvesTheImplicitLatestTag は、タグを省略したモデル指定が
// ":latest" と照合されることを見る。
//
// 🔴 これが無いと照合は必ず外れる。設定は RECALL_EMBED_MODEL=bge-m3 と書くが、
// /api/tags が返す name は "bge-m3:latest" である（実測）。偽サーバの一覧にも
// タグ無しの名前は入れていないので、":latest" を補わない実装ではここが空になる。
func TestRuntimeResolvesTheImplicitLatestTag(t *testing.T) {
	server := newRecordingOllama()
	server.tags = []fakeTag{
		{Name: "fake-model:latest", Model: "fake-model:latest", Digest: strings.Repeat("c", 64)},
	}

	client := startFakeOllama(t, server, 32)

	got, err := client.Runtime(t.Context())
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	if want := strings.Repeat("c", 64); got.Digest != want {
		t.Errorf("Digest = %q, want %q（:latest の補完が効いていない）", got.Digest, want)
	}
}

// TestRuntimeKeepsAnExplicitTag は、明示されたタグに ":latest" を足さないことを見る。
func TestRuntimeKeepsAnExplicitTag(t *testing.T) {
	server := newRecordingOllama()
	server.tags = []fakeTag{
		{Name: "pinned:v2", Model: "pinned:v2", Digest: strings.Repeat("d", 64)},
		{Name: "pinned:latest", Model: "pinned:latest", Digest: strings.Repeat("e", 64)},
	}

	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	client, err := ollama.New(ollama.Config{
		BaseURL:    srv.URL,
		Model:      "pinned:v2",
		Dimensions: testDimensions,
		BatchSize:  32,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}

	got, err := client.Runtime(t.Context())
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	if want := strings.Repeat("d", 64); got.Digest != want {
		t.Errorf("Digest = %q, want %q（明示タグが :latest に潰された）", got.Digest, want)
	}
}

// TestRuntimeReturnsAnEmptyDigestWhenTheModelIsAbsent は、一覧に無いモデルで
// 失敗にしないことを見る。
//
// ⚠️ digest はレポートの付帯情報であって、評価そのものを止める理由にならない。
// 取得できなかったことは「空である」という形でレポートに残る。
func TestRuntimeReturnsAnEmptyDigestWhenTheModelIsAbsent(t *testing.T) {
	server := newRecordingOllama()
	server.tags = []fakeTag{
		{Name: "other:latest", Model: "other:latest", Digest: strings.Repeat("f", 64)},
	}

	client := startFakeOllama(t, server, 32)

	got, err := client.Runtime(t.Context())
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}

	if got.Digest != "" {
		t.Errorf("Digest = %q, want 空", got.Digest)
	}

	// 版のほうは取れていること。片方の欠落がもう片方を巻き込まない。
	if got.Version != "0.33.2" {
		t.Errorf("Version = %q, want %q", got.Version, "0.33.2")
	}
}

// TestRuntimeFailsWhenOllamaIsUnreachable は、到達できないときに
// embed.ErrProviderUnavailable を返すことを見る。
func TestRuntimeFailsWhenOllamaIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(newRecordingOllama())
	url := srv.URL

	srv.Close() // 閉じてから叩く

	client := newClient(t, url, 32, stubHTTPClient())

	if _, err := client.Runtime(t.Context()); !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestRuntimeFailsOnABrokenResponse は、JSON でない応答を黙って空として
// 扱わないことを見る。
//
// 空の版・空の digest が「取れなかった」と「そう返ってきた」の両方を意味すると、
// レポートの読み手は区別できない。
func TestRuntimeFailsOnABrokenResponse(t *testing.T) {
	server := newRecordingOllama()
	server.rawBody = "<html>proxy error</html>"

	client := startFakeOllama(t, server, 32)

	if _, err := client.Runtime(t.Context()); !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestRuntimeFailsOnAnErrorStatus は、非 200 を失敗として扱うことを見る。
func TestRuntimeFailsOnAnErrorStatus(t *testing.T) {
	server := newRecordingOllama()
	server.status = http.StatusInternalServerError

	client := startFakeOllama(t, server, 32)

	if _, err := client.Runtime(t.Context()); !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}
