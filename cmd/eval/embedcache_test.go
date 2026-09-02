package main_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/eval"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// errCountingEmbedder は偽物が意図的に失敗するときの理由 (GO-005)。
var errCountingEmbedder = errors.New("fake: injected failure")

// countingEmbedder は呼ばれた本文を覚える偽の Embedder。
//
// 🔑 「何本を下層へ渡したか」が検査の中心である。件数だけを数えると、
// キャッシュを無視して毎回全部渡す実装でもヒット数の辻褄が合ってしまう。
type countingEmbedder struct {
	id   string
	dims int
	// seen は Embed に渡された本文を、呼ばれた順に平らに並べたもの。
	seen []string
	// calls は Embed が呼ばれた回数。
	calls int
	// err が非 nil なら Embed が失敗する。
	err error
}

// newCountingEmbedder は既定の偽物を作る。
func newCountingEmbedder() *countingEmbedder {
	return &countingEmbedder{id: "fake:4", dims: 4, seen: nil, calls: 0, err: nil}
}

// Embed は本文の長さから決まるベクトルを返す。
func (c *countingEmbedder) Embed(
	_ context.Context, texts []string, _ embed.Kind,
) ([][]float32, error) {
	c.calls++

	if c.err != nil {
		return nil, c.err
	}

	out := make([][]float32, 0, len(texts))

	for _, text := range texts {
		c.seen = append(c.seen, text)

		vector := make([]float32, c.dims)
		for i := range vector {
			vector[i] = float32(len(text) + i)
		}

		out = append(out, vector)
	}

	return out, nil
}

// Dimensions は次元数を返す。
func (c *countingEmbedder) Dimensions() int { return c.dims }

// ID は識別子を返す。
func (c *countingEmbedder) ID() string { return c.id }

// documents はテストで使う本文。
func documents() []string { return []string{"あ", "いい", "ううう"} }

// TestEmbedCacheReusesVectorsAcrossRuns は2回目が下層を呼ばないことを見る。
//
// 🔴 これが ADR 0019 Decision 3 の中身である。10万件の埋め込みは約18分かかり、
// 毎回やり直すと alpha 掃引も before/after も現実的でなくなる。
func TestEmbedCacheReusesVectorsAcrossRuns(t *testing.T) {
	dir := t.TempDir()

	first := newCountingEmbedder()

	cache, err := main.NewCachingEmbedder(first, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	want, err := cache.Embed(t.Context(), documents(), embed.KindDocument)
	if err != nil {
		t.Fatalf("1回目の Embed: %v", err)
	}

	if cache.Hits() != 0 || cache.Misses() != int64(len(documents())) {
		t.Fatalf("1回目 hits=%d misses=%d, want 0 / %d",
			cache.Hits(), cache.Misses(), len(documents()))
	}

	// 2回目は別のプロセスを模して新しいラッパから引く。
	second := newCountingEmbedder()

	reopened, err := main.NewCachingEmbedder(second, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	got, err := reopened.Embed(t.Context(), documents(), embed.KindDocument)
	if err != nil {
		t.Fatalf("2回目の Embed: %v", err)
	}

	if second.calls != 0 {
		t.Errorf("2回目に下層を %d 回呼んだ。キャッシュから返っていない", second.calls)
	}

	if reopened.Hits() != int64(len(documents())) || reopened.Misses() != 0 {
		t.Errorf("2回目 hits=%d misses=%d, want %d / 0",
			reopened.Hits(), reopened.Misses(), len(documents()))
	}

	assertSameVectors(t, got, want)
}

// TestEmbedCacheNeverCachesQueries はクエリ側をキャッシュしないことを見る。
//
// 🔴 系統1（埋め込み往復を含む）の latency はクエリ側の埋め込みで測る。
// キャッシュすると系統1 が系統2 と同じになり、2系統に分けた意味が消える
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 3)。
func TestEmbedCacheNeverCachesQueries(t *testing.T) {
	dir := t.TempDir()
	inner := newCountingEmbedder()

	cache, err := main.NewCachingEmbedder(inner, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	for range 2 {
		if _, err := cache.Embed(t.Context(), []string{"問い"}, embed.KindQuery); err != nil {
			t.Fatalf("Embed: %v", err)
		}
	}

	if inner.calls != 2 {
		t.Errorf("下層の呼び出し = %d, want 2（クエリは毎回埋め込み直す）", inner.calls)
	}

	if cache.Hits() != 0 || cache.Misses() != 0 {
		t.Errorf("hits=%d misses=%d, want 0 / 0（クエリは件数にも数えない）",
			cache.Hits(), cache.Misses())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("クエリのベクトルがディスクに残った: %v", entries)
	}
}

// TestEmbedCachePassesOnlyTheMisses は当たったぶんを下層へ渡さないことと、
// 並びが入力どおりに戻ることを見る。
func TestEmbedCachePassesOnlyTheMisses(t *testing.T) {
	dir := t.TempDir()
	inner := newCountingEmbedder()

	cache, err := main.NewCachingEmbedder(inner, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	if _, err := cache.Embed(t.Context(), []string{"いい"}, embed.KindDocument); err != nil {
		t.Fatalf("温め: %v", err)
	}

	inner.seen = nil

	got, err := cache.Embed(t.Context(), documents(), embed.KindDocument)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// 2件目は既にディスクにあるので、下層へは1件目と3件目だけが渡る。
	if len(inner.seen) != 2 || inner.seen[0] != "あ" || inner.seen[1] != "ううう" {
		t.Fatalf("下層へ渡した本文 = %v, want [あ ううう]", inner.seen)
	}

	// 穴埋めの結果が入力と同じ並びに戻っていること。
	for i, text := range documents() {
		if got[i][0] != float32(len(text)) {
			t.Errorf("got[%d] が %q のベクトルではない: %v", i, text, got[i])
		}
	}
}

// TestEmbedCacheKeyIncludesTheEmbedderID はモデルを替えたら別のキャッシュに
// なることを見る。
//
// 🔴 次元が一致していても異なるモデルのベクトルは比較できず、放置すると
// 「エラーにならないまま無意味なスコアが返る」(ADR 0005)。鍵に ID を混ぜて
// あるので、この事故は起こしようがない。
func TestEmbedCacheKeyIncludesTheEmbedderID(t *testing.T) {
	dir := t.TempDir()

	first := newCountingEmbedder()

	warm, err := main.NewCachingEmbedder(first, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	if _, err := warm.Embed(t.Context(), documents(), embed.KindDocument); err != nil {
		t.Fatalf("温め: %v", err)
	}

	other := newCountingEmbedder()
	other.id = "another:4"

	cache, err := main.NewCachingEmbedder(other, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	if _, err := cache.Embed(t.Context(), documents(), embed.KindDocument); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if cache.Hits() != 0 {
		t.Errorf("別モデルなのに %d 件当たった。鍵に embedder_id が入っていない", cache.Hits())
	}

	if cache.ID() != "another:4" {
		t.Errorf("ID() = %q。ラッパは下層の識別子をそのまま名乗ること", cache.ID())
	}
}

// TestEmbedCacheIgnoresTruncatedFiles は途中で切れたファイルを使わないことを見る。
//
// 🔑 短いファイルをそのまま復号すると、次元の足りないベクトルが検索へ流れ、
// ストア側が「正規化されていない」と報告する——原因がキャッシュだとは
// 分からない壊れ方になる。
func TestEmbedCacheIgnoresTruncatedFiles(t *testing.T) {
	dir := t.TempDir()
	inner := newCountingEmbedder()

	cache, err := main.NewCachingEmbedder(inner, dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	if _, err := cache.Embed(t.Context(), []string{"あ"}, embed.KindDocument); err != nil {
		t.Fatalf("温め: %v", err)
	}

	truncateCacheFiles(t, dir)

	reopened, err := main.NewCachingEmbedder(newCountingEmbedder(), dir)
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	if _, err := reopened.Embed(t.Context(), []string{"あ"}, embed.KindDocument); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if reopened.Hits() != 0 || reopened.Misses() != 1 {
		t.Errorf("hits=%d misses=%d, want 0 / 1（切れたファイルは無かったことにする）",
			reopened.Hits(), reopened.Misses())
	}
}

// TestEmbedCacheReportsInnerFailures は下層の失敗を握り潰さないことを見る。
func TestEmbedCacheReportsInnerFailures(t *testing.T) {
	inner := newCountingEmbedder()
	inner.err = errCountingEmbedder

	cache, err := main.NewCachingEmbedder(inner, t.TempDir())
	if err != nil {
		t.Fatalf("NewCachingEmbedder: %v", err)
	}

	for name, kind := range map[string]embed.Kind{
		"取り込み": embed.KindDocument,
		"クエリ":  embed.KindQuery,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cache.Embed(t.Context(), []string{"あ"}, kind)
			if !errors.Is(err, errCountingEmbedder) {
				t.Fatalf("err = %v, want errCountingEmbedder", err)
			}
		})
	}
}

// truncateCacheFiles はキャッシュのファイルを1バイトに切り詰める。
func truncateCacheFiles(t *testing.T, dir string) {
	t.Helper()

	found := 0

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		found++

		return os.WriteFile(path, []byte{0}, 0o600)
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}

	if found == 0 {
		t.Fatal("キャッシュのファイルが1つも作られていない")
	}
}

// assertSameVectors は2つのベクトル列が一致することを確かめる。
func assertSameVectors(t *testing.T, got, want [][]float32) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("本数 = %d, want %d", len(got), len(want))
	}

	for i := range got {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("got[%d] の次元 = %d, want %d", i, len(got[i]), len(want[i]))
		}

		for j := range got[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("got[%d][%d] = %v, want %v", i, j, got[i][j], want[i][j])
			}
		}
	}
}
