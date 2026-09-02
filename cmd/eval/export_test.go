package main

import (
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// このファイルはテストのためだけの公開を1箇所に閉じるためにある (GO-008 / QLT-006)。
// cmd/eval は package main なので、外部テストパッケージ (main_test) からは
// 未公開の宣言が見えない。本体側で export せず、ここだけで橋を架ける。

// AlphaNoteFor は store 名から alpha の但し書きを選ぶ経路をテストへ開く。
func AlphaNoteFor(store string) (string, error) { return alphaNote(store) }

// AlphaNotePostgres は postgres 向けの但し書き。期待値の突き合わせに使う。
func AlphaNotePostgres() string { return alphaNotePostgres }

// AlphaNoteSQLite は sqlite 向けの但し書き。期待値の突き合わせに使う。
func AlphaNoteSQLite() string { return alphaNoteSQLite }

// DecimalOfFloat32 は float32 の重みを10進へ戻す経路をテストへ開く。
func DecimalOfFloat32(v float32) (float64, error) { return decimalOfFloat32(v) }

// ErrUnknownStore は未知のストア名を表す sentinel。errors.Is での分岐を
// テストから確かめるために開く (GO-005)。
func ErrUnknownStore() error { return errUnknownStore }

// CachingEmbedder は埋め込みのディスクキャッシュ。テストから触るための別名で、
// 本体側は未公開のまま保つ (GO-008)。
type CachingEmbedder = cachingEmbedder

// NewCachingEmbedder はキャッシュの組み立てをテストへ開く。
func NewCachingEmbedder(inner embed.Embedder, dir string) (*CachingEmbedder, error) {
	return newCachingEmbedder(inner, dir)
}

// DistractorCount は記録から件数を取り出す経路をテストへ開く。
func DistractorCount(record *eval.FileInput) int { return distractorCount(record) }

// LoadDistractorsAt は -distractors の読み込みをテストへ開く。
// 返すのは件数と記録だけで、中身は internal/eval 側のテストが見る。
func LoadDistractorsAt(path string) (int, *eval.FileInput, error) {
	set, err := loadDistractors(path)
	if err != nil {
		return 0, nil, err
	}

	return len(set.items), set.record, nil
}
