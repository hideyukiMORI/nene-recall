package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// 🔴 cachingEmbedder が internal/embed ではなくここにあるのは、あちらが契約
// パッケージだからである。ARC-002 は internal/embed に os を持ち込ませない
// （中核は環境・ファイルを触らない）。ディスクを使う実装は配線点に置く
// (docs/adr/0012-embedding-implementations-live-in-subpackages.md と同じ非対称)。
//
// 🔴 これは評価ハーネス専用である。cmd/recall には配線しない。サーバの
// 埋め込みをディスクに貯めると、モデルを替えたときの不一致検知 (ADR 0005) が
// 効く前に古いベクトルが返る経路ができる。評価では毎回 DB を作り直すので、
// キャッシュの寿命は「同じモデル・同じ本文」に閉じている。

// cacheFileMode / cacheDirMode はキャッシュの権限。
const (
	cacheFileMode = 0o644
	cacheDirMode  = 0o755
)

// cacheShardWidth はディレクトリを分ける鍵の先頭文字数。
//
// 10万件を1つのディレクトリに置くと、ファイル一覧の操作が実用に耐えなくなる。
// 2文字で 256 に分かれるので、1シャードあたり数百件に収まる。
const cacheShardWidth = 2

// float32Bytes は float32 1個ぶんのバイト数。
const float32Bytes = 4

var (
	// errEmbedCache はキャッシュの読み書きに失敗したことを表す。
	errEmbedCache = errors.New("eval: the embedding cache failed")
	// errEmbedCacheCount は下層の Embedder が要求と違う本数を返したことを表す。
	errEmbedCacheCount = errors.New("eval: the embedder returned a different number of vectors")
)

// cachingEmbedder は取り込み側の埋め込みをディスクへ貯めて再利用する。
//
// 🔴 Kind が Query のものはキャッシュしない。系統1（埋め込み往復を含む）の
// latency はクエリ側の埋め込みで測るので、キャッシュすると系統1 が系統2 と
// 同じものになり、2系統に分けた意味が消える
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 3)。
//
// ゼロ値は無効である。newCachingEmbedder を通すこと。
type cachingEmbedder struct {
	inner embed.Embedder
	dir   string
	// id は鍵に混ぜる Embedder.ID()。
	//
	// 🔴 鍵に含めるので、モデルを替えれば自動的に別のキャッシュになる。
	// 含めないと「次元は同じだが別モデルのベクトル」が返り、エラーにならない
	// まま無意味なスコアが出る (ADR 0005)。
	id string
	// hits / misses は件数の記録。原子的に数えるのは、Embedder の契約が
	// 単一スレッドを約束していないためである。
	hits   atomic.Int64
	misses atomic.Int64
}

// newCachingEmbedder は置き場所を用意してラッパを作る。
func newCachingEmbedder(inner embed.Embedder, dir string) (*cachingEmbedder, error) {
	if err := os.MkdirAll(dir, cacheDirMode); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", errEmbedCache, dir, err)
	}

	return &cachingEmbedder{
		inner:  inner,
		dir:    dir,
		id:     inner.ID(),
		hits:   atomic.Int64{},
		misses: atomic.Int64{},
	}, nil
}

// Dimensions は下層の次元数をそのまま返す。
func (c *cachingEmbedder) Dimensions() int { return c.inner.Dimensions() }

// ID は下層の識別子をそのまま返す。
//
// 🔴 ラッパであることを名乗らない。ストアはこの値を embedder_id 列に書き、
// 次回の投入で一致を検査する (ADR 0005)。キャッシュの有無で値が変わると、
// 同じモデルなのに「モデルが変わった」と判定される。
func (c *cachingEmbedder) ID() string { return c.inner.ID() }

// Hits はキャッシュから返した本数。
func (c *cachingEmbedder) Hits() int64 { return c.hits.Load() }

// Misses は下層へ問い合わせた本数。
func (c *cachingEmbedder) Misses() int64 { return c.misses.Load() }

// Embed はキャッシュに無いものだけを下層へ渡す。
func (c *cachingEmbedder) Embed(
	ctx context.Context, texts []string, kind embed.Kind,
) ([][]float32, error) {
	if kind == embed.KindQuery {
		vectors, err := c.inner.Embed(ctx, texts, kind)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}

		return vectors, nil
	}

	vectors, missing := c.fromCache(texts, kind)
	if len(missing) == 0 {
		return vectors, nil
	}

	return c.fill(ctx, texts, kind, cacheGap{vectors: vectors, missing: missing})
}

// cacheGap は埋め戻しの途中経過。引数を4つ以下に保つための入れ物 (GO-011)。
type cacheGap struct {
	// vectors はキャッシュから埋まったぶん。穴は nil のまま残っている。
	vectors [][]float32
	// missing は穴の添字。texts と vectors の両方に同じ添字で対応する。
	missing []int
}

// fromCache はキャッシュから引けるものを埋め、引けなかった添字を返す。
func (c *cachingEmbedder) fromCache(texts []string, kind embed.Kind) ([][]float32, []int) {
	vectors := make([][]float32, len(texts))

	var missing []int

	for i, text := range texts {
		vector, ok := c.load(c.key(kind, text))
		if !ok {
			missing = append(missing, i)

			continue
		}

		vectors[i] = vector

		c.hits.Add(1)
	}

	return vectors, missing
}

// fill は穴のぶんだけ下層を呼び、結果をキャッシュへ書いてから埋める。
func (c *cachingEmbedder) fill(
	ctx context.Context, texts []string, kind embed.Kind, gap cacheGap,
) ([][]float32, error) {
	pending := make([]string, 0, len(gap.missing))
	for _, i := range gap.missing {
		pending = append(pending, texts[i])
	}

	fresh, err := c.inner.Embed(ctx, pending, kind)
	if err != nil {
		return nil, fmt.Errorf("embed documents: %w", err)
	}

	if len(fresh) != len(pending) {
		return nil, fmt.Errorf("%w: got %d for %d texts",
			errEmbedCacheCount, len(fresh), len(pending))
	}

	for j, i := range gap.missing {
		if err := c.store(c.key(kind, texts[i]), fresh[j]); err != nil {
			return nil, err
		}

		gap.vectors[i] = fresh[j]

		c.misses.Add(1)
	}

	return gap.vectors, nil
}

// key は鍵を16進で返す。
//
// 🔴 embedder_id・kind・本文の3つを NUL で区切って混ぜる。区切りを入れないと
// ("bge-m3:1024", "query" + text) と ("bge-m3:1024q", "uery" + text) が同じ
// 鍵になる。NUL は本文にも ID にも現れない
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 3)。
func (c *cachingEmbedder) key(kind embed.Kind, text string) string {
	sum := sha256.Sum256([]byte(c.id + "\x00" + string(kind) + "\x00" + text))

	return hex.EncodeToString(sum[:])
}

// path は鍵からファイルの場所を決める。先頭 2 文字でディレクトリを分ける。
func (c *cachingEmbedder) path(key string) string {
	return filepath.Join(c.dir, key[:cacheShardWidth], key)
}

// load はキャッシュから1本読む。読めなければ「無かった」として扱う。
//
// 🔴 失敗を error にしない。キャッシュは速度のための仕組みであって、
// 壊れたファイルや途中で切れた書き込みは埋め込み直せば済む。error にすると、
// 消せば直るものが計測を止めることになる。
//
// 🔑 長さを検査するのは、途中で切れた書き込みを黙って使わないためである。
// 短いファイルをそのまま復号すると、次元の足りないベクトルが検索へ流れ、
// ストアの validateVector が「正規化されていない」と報告する——原因が
// キャッシュだとは分からない壊れ方になる。
func (c *cachingEmbedder) load(key string) ([]float32, bool) {
	raw, err := os.ReadFile(c.path(key))
	if err != nil || len(raw) != c.Dimensions()*float32Bytes {
		return nil, false
	}

	vector := make([]float32, c.Dimensions())
	for i := range vector {
		vector[i] = math.Float32frombits(
			binary.LittleEndian.Uint32(raw[i*float32Bytes:]))
	}

	return vector, true
}

// store はキャッシュへ1本書く。
//
// 🔴 次元の違うベクトルは書かない。書いてしまうと load の長さ検査が通らず、
// 永久に当たらないファイルが溜まる。
func (c *cachingEmbedder) store(key string, vector []float32) error {
	if len(vector) != c.Dimensions() {
		return fmt.Errorf("%w: vector has %d dimensions, want %d",
			errEmbedCache, len(vector), c.Dimensions())
	}

	raw := make([]byte, len(vector)*float32Bytes)
	for i, v := range vector {
		binary.LittleEndian.PutUint32(raw[i*float32Bytes:], math.Float32bits(v))
	}

	shard := filepath.Dir(c.path(key))
	if err := os.MkdirAll(shard, cacheDirMode); err != nil {
		return fmt.Errorf("%w: create %s: %w", errEmbedCache, shard, err)
	}

	if err := os.WriteFile(c.path(key), raw, cacheFileMode); err != nil {
		return fmt.Errorf("%w: write %s: %w", errEmbedCache, key, err)
	}

	return nil
}
