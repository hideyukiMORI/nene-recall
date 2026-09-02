package sqlite_test

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// TestEncodeVectorRoundTrip は、BLOB への符号化と復号が値を1ビットも
// 変えないことを見る。
//
// 🔑 テキストではなくバイト列を選んだ理由がこれである。丸めが入る形式だと、
// 保存したベクトルと読んだベクトルの内積が、元の内積とわずかに違う。
// 順位が僅差の行で結果が変わり、その差は誰にも説明できない。
func TestEncodeVectorRoundTrip(t *testing.T) {
	v := make([]float32, sqlite.VectorDimensions)
	for i := range v {
		// 端数の多い値を並べる。0.1 刻みのような「たまたま丸めが効かない」
		// 値では往復の検証にならない。
		v[i] = float32(math.Sin(float64(i) * 0.7))
	}

	blob := sqlite.EncodeVector(v)
	if len(blob) != sqlite.VectorBytes {
		t.Fatalf("BLOB の長さ = %d, want %d", len(blob), sqlite.VectorBytes)
	}

	got, err := sqlite.DecodeVector(blob)
	if err != nil {
		t.Fatalf("DecodeVector: %v", err)
	}

	if !slices.Equal(got, v) {
		t.Errorf("🔴 往復で値が変わった")
	}
}

// TestDecodeVectorRejectsBadLength は、長さの違う BLOB を読まないことを見る。
func TestDecodeVectorRejectsBadLength(t *testing.T) {
	cases := map[string][]byte{
		"空":         {},
		"短い":        {1, 2, 3},
		"4 の倍数だが短い": make([]byte, 8),
		"長い":        make([]byte, sqlite.VectorBytes+4),
	}

	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := sqlite.DecodeVector(blob); !errors.Is(err, sqlite.ErrVectorBlobLength()) {
				t.Errorf("err = %v, want ErrVectorBlobLength", err)
			}
		})
	}
}

// TestIDFilterJSON は、nil と空スライスが同じ「フィルタ無し」になることを見る。
//
// 🔴 SQL 側は json_array_length(...) = 0 を「フィルタ無し」と読む。この対応が
// 崩れると、絞り込まないつもりの検索が全滅する。
func TestIDFilterJSON(t *testing.T) {
	cases := []struct {
		name string
		ids  []int64
		want string
	}{
		{name: "nil", ids: nil, want: "[]"},
		{name: "空", ids: []int64{}, want: "[]"},
		{name: "1件", ids: []int64{7}, want: "[7]"},
		{name: "複数", ids: []int64{7, 8, 9}, want: "[7,8,9]"},
		{name: "負の値も通す", ids: []int64{-1}, want: "[-1]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlite.IDFilterJSON(tc.ids); got != tc.want {
				t.Errorf("IDFilterJSON(%v) = %q, want %q", tc.ids, got, tc.want)
			}
		})
	}
}

// TestEncodeMatchExpression は、MATCH 式の組み立てを見る。
//
// 🔴 空のトークン列が空文字になることが重要である。空文字を SQLite に渡すと
// 構文エラーになるので、呼び出し側が分岐する契約になっている。
func TestEncodeMatchExpression(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
		want   string
	}{
		{name: "空は空文字", tokens: []string{}, want: ""},
		{name: "1語", tokens: []string{"alpha"}, want: `"alpha"`},
		{name: "2語は OR", tokens: []string{"alpha", "bravo"}, want: `"alpha" OR "bravo"`},
		{name: "下線はそのまま囲む", tokens: []string{"recall_store"}, want: `"recall_store"`},
		{name: "CJK もそのまま囲む", tokens: []string{"検索", "索対"}, want: `"検索" OR "索対"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sqlite.EncodeMatchExpression(tc.tokens)
			if err != nil {
				t.Fatalf("EncodeMatchExpression: %v", err)
			}

			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEncodeRejectsBrokenTokens は、契約を破るトークンが符号化を通らないことを見る。
func TestEncodeRejectsBrokenTokens(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  error
	}{
		{name: "半角空白", token: "a b", want: sqlite.ErrTokenHasWhitespace()},
		{name: "全角空白", token: "a　b", want: sqlite.ErrTokenHasWhitespace()},
		{name: "改行", token: "a\nb", want: sqlite.ErrTokenHasWhitespace()},
		{name: "二重引用符", token: `a"b`, want: sqlite.ErrTokenHasMetaCharacter()},
		{name: "アスタリスク", token: "a*", want: sqlite.ErrTokenHasMetaCharacter()},
		{name: "丸括弧", token: "a(b", want: sqlite.ErrTokenHasMetaCharacter()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sqlite.EncodeMatchExpression([]string{tc.token}); !errors.Is(err, tc.want) {
				t.Errorf("EncodeMatchExpression の err = %v, want %v", err, tc.want)
			}

			if _, err := sqlite.EncodeLexemeText([]string{tc.token}); !errors.Is(err, tc.want) {
				t.Errorf("EncodeLexemeText の err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestValidateVector は、次元と正規化の検査が働くことを見る。
func TestValidateVector(t *testing.T) {
	t.Run("正規化済みは通る", func(t *testing.T) {
		if err := sqlite.ValidateVector(planeVector(0.3)); err != nil {
			t.Errorf("ValidateVector: %v", err)
		}
	})

	t.Run("次元が足りない", func(t *testing.T) {
		if err := sqlite.ValidateVector([]float32{1, 0}); !errors.Is(err, sqlite.ErrVectorDimensions()) {
			t.Errorf("err = %v, want ErrVectorDimensions", err)
		}
	})

	t.Run("正規化されていない", func(t *testing.T) {
		err := sqlite.ValidateVector(scaleVector(planeVector(0), 2))
		if !errors.Is(err, sqlite.ErrVectorNotNormalized()) {
			t.Errorf("err = %v, want ErrVectorNotNormalized", err)
		}
	})

	t.Run("許容幅の内側は通る", func(t *testing.T) {
		// 二乗ノルムが 1 + 幅/2 になる倍率。境界の内側で通ることを見る。
		scale := math.Sqrt(1 + sqlite.NormToleranceSquared/2)
		if err := sqlite.ValidateVector(scaleVector(planeVector(0), scale)); err != nil {
			t.Errorf("許容幅の内側で落ちた: %v", err)
		}
	})
}

// TestEncodeLexemeText は、トークン列が空白区切りで並ぶことを見る。
func TestEncodeLexemeText(t *testing.T) {
	got, err := sqlite.EncodeLexemeText([]string{"alpha", "bravo"})
	if err != nil {
		t.Fatalf("EncodeLexemeText: %v", err)
	}

	if got != "alpha bravo" {
		t.Errorf("= %q, want %q", got, "alpha bravo")
	}

	empty, err := sqlite.EncodeLexemeText(nil)
	if err != nil {
		t.Fatalf("EncodeLexemeText(nil): %v", err)
	}

	if empty != "" {
		t.Errorf("空のトークン列 = %q, want 空文字", empty)
	}
}
