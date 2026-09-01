// Package org はテナントの識別子を表す。
//
// 🔴 この型が存在する理由は、org_id の取り違えが「静かに」情報漏洩になるからである
// (docs/adr/0003-org-id-is-mandatory.md)。int64 のまま持ち回ると、別の識別子を渡しても、
// 既定値の 0 を渡しても、コンパイラは何も言わない。名前付き型にすると変数の取り違えは
// コンパイルエラーになる。
//
// ただし Go の型システムだけでは塞ぎきれない穴が二つ残る。
//
//	org.ID(1)   // 未型付き定数からの変換は通る
//	var id org.ID // ゼロ値は常に作れる
//
// 前者は tools/conformance の CNF-001 が禁止する。後者は言語仕様上防げないので、
// 「ゼロ値は無効値」を型の契約として宣言し、境界で必ず検証する。
// 詳細は docs/coding-rules.md の GO-001 / GO-003 を参照。
package org

import (
	"errors"
	"fmt"
	"strconv"
)

// ErrInvalid は org の識別子として受け付けられない値だったことを表す。
//
// 呼び出し側は errors.Is で判定する。メッセージ文字列で分岐しないこと。
var ErrInvalid = errors.New("org: invalid organization id")

// ID はテナントの識別子。
//
// 🔴 ゼロ値は無効値である。「org_id が無いとき」を表す ID は存在しない。
// 未指定は ID のゼロ値ではなく error として扱う (ADR 0003)。
// 「既定の org」という概念をこの型に持ち込まないこと。
type ID int64

// NewID は正の整数から ID を作る。
//
// 0 以下を拒否するのは、ゼロ値が「未指定」として紛れ込む経路を潰すため。
func NewID(v int64) (ID, error) {
	if v < 1 {
		return 0, fmt.Errorf("%w: must be a positive integer, got %d", ErrInvalid, v)
	}

	return ID(v), nil
}

// ParseID は外部入力（クエリ文字列・JSON）の文字列から ID を作る。
//
// 空文字・非数値・0 以下をすべて同じ error として拒否する。
// 「空なら既定値」という分岐をここに書かないこと。それが ADR 0003 の禁止事項そのものである。
func ParseID(raw string) (ID, error) {
	if raw == "" {
		return 0, fmt.Errorf("%w: org_id is required", ErrInvalid)
	}

	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: org_id must be an integer, got %q", ErrInvalid, raw)
	}

	return NewID(v)
}

// Int64 は永続化層へ渡すための生の値を返す。
//
// この変換は「ID から出る」方向にしか使わない。逆方向は NewID / ParseID だけを通す。
func (id ID) Int64() int64 { return int64(id) }

// String は識別子を10進表記で返す。
func (id ID) String() string { return strconv.FormatInt(int64(id), 10) }
