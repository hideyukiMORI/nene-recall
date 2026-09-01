package postgres_test

import (
	"errors"
	"math"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// normalizedVector は正規化済みベクトルを作る。
//
// 全要素を 1/32 にすると二乗和は 1024 * (1/32)^2 = 1.0 ちょうどになる。
// 1/32 は float32 で正確に表現できるので、この期待値に丸め誤差が混じらない。
func normalizedVector() []float32 {
	v := make([]float32, postgres.VectorDimensions)
	for i := range v {
		v[i] = 1.0 / 32.0
	}

	return v
}

// vectorWithSquaredNorm は二乗和が target になるベクトルを作る。
//
// 1要素だけを動かして残り 1023 要素は 1/32 のままにする。
// 許容幅の内側と外側を、閾値の数値を書き写さずに作るための道具。
func vectorWithSquaredNorm(t *testing.T, target float64) []float32 {
	t.Helper()

	v := normalizedVector()
	rest := float64(postgres.VectorDimensions-1) / float64(postgres.VectorDimensions)
	v[0] = float32(math.Sqrt(target - rest))

	return v
}

func TestValidateVectorAcceptsNormalizedVector(t *testing.T) {
	if err := postgres.ValidateVector(normalizedVector()); err != nil {
		t.Fatalf("正規化済みベクトルが拒否された: %v", err)
	}
}

// TestValidateVectorRejectsWrongDimensions は次元検査が実際に発火することを示す。
//
// 検査器の最大の失敗は、見逃したまま常に緑を返すことである（QLT-006）。
func TestValidateVectorRejectsWrongDimensions(t *testing.T) {
	for _, length := range []int{0, 1, postgres.VectorDimensions - 1, postgres.VectorDimensions + 1} {
		err := postgres.ValidateVector(make([]float32, length))
		if err == nil {
			t.Fatalf("次元 %d が受け入れられた", length)
		}

		if !errors.Is(err, postgres.ErrVectorDimensions()) {
			t.Errorf("次元 %d: 次元違反として報告されていない: %v", length, err)
		}

		if !errors.Is(err, postgres.ErrVectorInvalid()) {
			t.Errorf("次元 %d: 上位の sentinel を辿れない: %v", length, err)
		}
	}
}

// TestValidateVectorRejectsUnnormalizedVector は正規化検査が発火することを示す。
//
// この検査が無いと、症状は「取り込みが失敗する」ではなく「順位が静かに狂う」になる。
func TestValidateVectorRejectsUnnormalizedVector(t *testing.T) {
	zero := make([]float32, postgres.VectorDimensions)

	doubled := normalizedVector()
	for i := range doubled {
		doubled[i] *= 2 // 二乗和は 4.0 になる
	}

	for name, v := range map[string][]float32{"零ベクトル": zero, "ノルム2倍": doubled} {
		err := postgres.ValidateVector(v)
		if err == nil {
			t.Fatalf("%s が受け入れられた", name)
		}

		if !errors.Is(err, postgres.ErrVectorNotNormalized()) {
			t.Errorf("%s: 正規化違反として報告されていない: %v", name, err)
		}

		if !errors.Is(err, postgres.ErrVectorInvalid()) {
			t.Errorf("%s: 上位の sentinel を辿れない: %v", name, err)
		}
	}
}

// TestValidateVectorHonorsTolerance は許容幅の内外で判定が分かれることを示す。
//
// 幅がゼロだと float32 の丸めだけで落ちるようになり、幅が広すぎると
// 契約違反を見逃す。境界の両側を押さえておく。
func TestValidateVectorHonorsTolerance(t *testing.T) {
	inside := vectorWithSquaredNorm(t, 1.0+postgres.NormToleranceSquared*0.9)
	if err := postgres.ValidateVector(inside); err != nil {
		t.Errorf("許容幅の内側が拒否された: %v", err)
	}

	outside := vectorWithSquaredNorm(t, 1.0+postgres.NormToleranceSquared*1.1)
	if err := postgres.ValidateVector(outside); !errors.Is(err, postgres.ErrVectorNotNormalized()) {
		t.Errorf("許容幅の外側が受け入れられた: %v", err)
	}
}
