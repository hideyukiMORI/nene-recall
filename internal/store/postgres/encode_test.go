package postgres_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

func TestEncodeVectorRendersPgvectorText(t *testing.T) {
	cases := []struct {
		name string
		in   []float32
		want string
	}{
		{name: "nil は空の表記になる", in: nil, want: "[]"},
		{name: "空スライスも空の表記になる", in: []float32{}, want: "[]"},
		{name: "1要素", in: []float32{0.5}, want: "[0.5]"},
		{name: "符号と零を保つ", in: []float32{-0.25, 0, 0.125}, want: "[-0.25,0,0.125]"},
		// pgvector のテキスト入力に確実に収めるため、指数表記へ逃がさない。
		{name: "小さい値でも指数表記にしない", in: []float32{1e-7}, want: "[0.0000001]"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := postgres.EncodeVector(c.in); got != c.want {
				t.Errorf("EncodeVector(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestEncodeVectorPreservesFloat32Precision は符号化が往復することを確かめる。
//
// 固定桁で丸めると埋め込みの下位ビットが静かに落ち、類似度の順位が変わりうる。
// 「落ちていない」ことは目視できないので、往復させて等値で見る。
func TestEncodeVectorPreservesFloat32Precision(t *testing.T) {
	in := []float32{float32(1) / 3, math.MaxFloat32, math.SmallestNonzeroFloat32, -0.1, 0.7}

	fields := strings.Split(strings.Trim(postgres.EncodeVector(in), "[]"), ",")
	if len(fields) != len(in) {
		t.Fatalf("要素数が変わった: got %d, want %d", len(fields), len(in))
	}

	for i, field := range fields {
		back, err := strconv.ParseFloat(field, 32)
		if err != nil {
			t.Fatalf("要素 %d (%q) を読み戻せない: %v", i, field, err)
		}

		if float32(back) != in[i] {
			t.Errorf("要素 %d が往復しない: got %v, want %v (表記 %q)", i, float32(back), in[i], field)
		}
	}
}

func TestEncodeInt64ArrayRendersPostgresArray(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want string
	}{
		// 🔴 nil と空を区別しない。どちらも「フィルタが空」という一つの意味になる。
		// 「フィルタ無し」をこの表記で表さないこと（encode.go のコメント参照）。
		{name: "nil は空配列になる", in: nil, want: "{}"},
		{name: "空スライスも空配列になる", in: []int64{}, want: "{}"},
		{name: "複数要素", in: []int64{1, 2, 3}, want: "{1,2,3}"},
		{name: "負の値", in: []int64{-1, 0}, want: "{-1,0}"},
		{
			name: "int64 の両端",
			in:   []int64{math.MinInt64, math.MaxInt64},
			want: "{-9223372036854775808,9223372036854775807}",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := postgres.EncodeInt64Array(c.in); got != c.want {
				t.Errorf("EncodeInt64Array(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
