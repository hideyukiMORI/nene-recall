package ollama

import (
	"fmt"
	"math"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// prepareVectors は応答のベクトル列を検証し、正規化して返す。
func (c *Client) prepareVectors(vectors [][]float32, want int) ([][]float32, error) {
	// 本数が合わないのは応答の破損である。順序の対応が崩れているので、
	// 少ないぶんだけ返すといった救済はしない。
	if len(vectors) != want {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs",
			embed.ErrProviderUnavailable, len(vectors), want)
	}

	for i, v := range vectors {
		if len(v) != c.dimensions {
			return nil, fmt.Errorf("%w: embeddings[%d] has %d dimensions, want %d",
				embed.ErrProviderUnavailable, i, len(v), c.dimensions)
		}

		if err := normalize(v); err != nil {
			return nil, fmt.Errorf("%w: embeddings[%d]", err, i)
		}
	}

	return vectors, nil
}

// normalize はベクトルを L2 ノルム 1 に整える（その場で書き換える）。
//
// 🔴 bge-m3 は正規化済みで返すが、それでもここで必ず正規化する。
// embed.Embedder の契約は「長さ1のベクトルを返すこと」であり、契約は
// プロバイダの現在の挙動に依存してはならない。Ollama の版やエンドポイントが
// 変わっても契約が無条件に成り立つようにするのが実装側の責任である。
//
// 🔴 これはストア側の validateVector を外してよい理由にはならない。
// ここは「契約を満たす」ための処理で、あちらは「契約が満たされているか」の検査
// である。二重になっているのは冗長ではなく設計で、別プロバイダを足したときに
// 検査だけが残る。
func normalize(v []float32) error {
	// 二乗和は float64 で積む。float32 のまま 1024 要素を足すと、誤差が
	// 検査の許容幅と同じ桁まで積み上がる。
	var sumSquares float64
	for _, x := range v {
		sumSquares += float64(x) * float64(x)
	}

	// 🔴 ゼロベクトルを 0 除算で通さない。通すと全要素が NaN になり、
	// 比較が常に false になって「最下位に沈むだけ」の行が静かに生まれる。
	// pgvector 側でもエラーにならないので、症状は検索順位の乱れとしてしか出ない。
	if sumSquares == 0 {
		return fmt.Errorf("%w: embedding is a zero vector", embed.ErrProviderUnavailable)
	}

	// NaN や Inf が混ざった応答も同じ理由で止める。
	if math.IsNaN(sumSquares) || math.IsInf(sumSquares, 0) {
		return fmt.Errorf("%w: embedding contains NaN or Inf", embed.ErrProviderUnavailable)
	}

	norm := float32(math.Sqrt(sumSquares))
	for i := range v {
		v[i] /= norm
	}

	return nil
}
