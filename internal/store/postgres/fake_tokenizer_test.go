package postgres_test

import "strings"

// fakeTokenizer は空白で割って小文字にするだけの偽実装。
//
// 🔑 ストアのテストの大半で実物（internal/lexical/bigram）ではなくこれを使う。
// ここで確かめたいのはストアの振る舞い（tokenizer_id の不一致検知・語彙経路の
// org 分離・合成の縮退）であって分割の品質ではない。実物を使うと、テストの
// 期待値が bigram の分割規則に依存し、分割規則を変えるたびに無関係なテストが
// 壊れる。
//
// 🔴 例外は往復同一性テスト（lexeme_roundtrip_test.go）である。あれは
// 「実物の出力が実 Postgres の中で期待どおりのレキシームになるか」を見る
// テストなので、偽実装で代用したら何も検証しないことになる。
type fakeTokenizer struct {
	id string
}

// newFakeTokenizer は識別子を指定して偽実装を作る。
func newFakeTokenizer(id string) fakeTokenizer {
	return fakeTokenizer{id: id}
}

// Tokenize は空白で割って小文字にする。
func (fakeTokenizer) Tokenize(text string) []string {
	return strings.Fields(strings.ToLower(text))
}

// ID は識別子を返す。
func (f fakeTokenizer) ID() string { return f.id }

// brokenTokenizer は契約を破るトークンを返す偽実装。
//
// lexical.Tokenizer の契約（空白と tsquery のメタ文字を含まない）は doc
// コメントにしか書けない。将来 Tokenizer を差し替える人が契約を破ったときに
// ストアが黙って壊れないことを、この実装で確かめる。
type brokenTokenizer struct {
	tokens []string
}

// Tokenize は入力に関わらず、仕込んだトークンをそのまま返す。
func (b brokenTokenizer) Tokenize(string) []string { return b.tokens }

// ID は識別子を返す。
func (brokenTokenizer) ID() string { return "broken:1" }
