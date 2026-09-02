package main

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
