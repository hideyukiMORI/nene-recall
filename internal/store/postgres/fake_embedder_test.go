package postgres_test

import (
	"context"
	"fmt"
	"math"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// fakeEmbedder は決定的な埋め込みを返す偽実装。
//
// 実 Ollama に依存させない。ここで確かめたいのはストアの振る舞い（分離・原子性・
// 順位付け）であって埋め込みの品質ではなく、外部プロセスに依存させると
// テストが「たまに落ちる」ものになる。
//
// ベクトルは e0-e1 平面上の単位ベクトルにしてある。角度差の余弦がそのまま
// 内積になるので、期待する順位を角度で書ける。
type fakeEmbedder struct {
	id     string
	dims   int
	angles map[string]float64
	// kinds は渡された Kind の記録。取り込みと検索で正しく使い分けているかを見る
	// （CLAUDE.md 地雷3: bge-m3 が無視する実装でも呼び出し側は必ず渡す）。
	kinds *[]embed.Kind
	// scale はベクトルの長さの倍率。1 以外にすると正規化の契約を破る実装になり、
	// validateVector が実際に発火することを確かめられる。
	scale float64
}

// newFakeEmbedder は既定の次元を持つ偽実装を作る。
func newFakeEmbedder(id string) *fakeEmbedder {
	return &fakeEmbedder{
		id:     id,
		dims:   postgres.VectorDimensions,
		angles: map[string]float64{},
		kinds:  &[]embed.Kind{},
		scale:  1,
	}
}

// Embed は登録した角度から単位ベクトルを作る。未登録の文字列は角度 0 になる。
func (f *fakeEmbedder) Embed(_ context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	*f.kinds = append(*f.kinds, kind)

	if kind != embed.KindDocument && kind != embed.KindQuery {
		return nil, fmt.Errorf("%w: %s", embed.ErrUnsupportedKind, kind)
	}

	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		out = append(out, scaleVector(planeVector(f.angles[text]), f.scale))
	}

	return out, nil
}

// Dimensions は次元数を返す。
func (f *fakeEmbedder) Dimensions() int { return f.dims }

// ID は識別子を返す。
func (f *fakeEmbedder) ID() string { return f.id }

// planeVector は先頭2成分だけを使う単位ベクトルを返す。
//
// cos^2 + sin^2 = 1 なので L2 ノルムは 1 になる。float32 への丸めで
// 1e-7 程度ずれるが、validateVector の許容幅 1e-3 の内側に収まる。
func planeVector(theta float64) []float32 {
	v := make([]float32, postgres.VectorDimensions)
	v[0] = float32(math.Cos(theta))
	v[1] = float32(math.Sin(theta))

	return v
}

// scaleVector は長さを倍率どおりに変える。scale が 1 なら何も変わらない。
func scaleVector(v []float32, scale float64) []float32 {
	if scale == 1 {
		return v
	}

	for i := range v {
		v[i] *= float32(scale)
	}

	return v
}
