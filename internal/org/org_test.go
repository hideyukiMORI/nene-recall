package org_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// TestParseIDRejectsEverythingThatIsNotAPositiveInteger は、外部入力から
// 有効な ID が作られる条件を固定する。
//
// 🔴 空文字を「既定の org」に読み替える実装が入っていないことが、このテストの主眼である。
// その実装は単一テナントで開発している限り一切症状を出さない (ADR 0003)。
func TestParseIDRejectsEverythingThatIsNotAPositiveInteger(t *testing.T) {
	rejected := map[string]string{
		"空文字":    "",
		"空白":     " ",
		"ゼロ":     "0",
		"負値":     "-1",
		"非数値":    "abc",
		"小数":     "1.5",
		"符号のみ":   "+",
		"前後に空白":  " 1 ",
		"16進表記":  "0x1",
		"巨大すぎる値": "99999999999999999999",
		"SQL 断片": "1 OR 1=1",
		"カンマ区切り": "1,2",
	}

	for name, raw := range rejected {
		t.Run(name, func(t *testing.T) {
			id, err := org.ParseID(raw)
			if !errors.Is(err, org.ErrInvalid) {
				t.Fatalf("ParseID(%q) = (%v, %v), want errors.Is(err, ErrInvalid)", raw, id, err)
			}

			if id != 0 {
				t.Fatalf("失敗時に ID %v が返った。ゼロ値以外を返してはいけない", id)
			}
		})
	}
}

// TestParseIDAcceptsPositiveIntegers は正常系を固定する。
func TestParseIDAcceptsPositiveIntegers(t *testing.T) {
	accepted := map[string]int64{
		"1":                   1,
		"42":                  42,
		"9223372036854775807": 9223372036854775807,
	}

	for raw, want := range accepted {
		t.Run(raw, func(t *testing.T) {
			id, err := org.ParseID(raw)
			if err != nil {
				t.Fatalf("ParseID(%q) が失敗した: %v", raw, err)
			}

			if id.Int64() != want {
				t.Fatalf("ParseID(%q).Int64() = %d, want %d", raw, id.Int64(), want)
			}
		})
	}
}

// TestNewIDRejectsZeroAndNegative は、ゼロ値が有効な ID にならないことを固定する。
//
// Go はゼロ値を常に作れる。だからこそ「0 は無効」を境界で明示的に拒否する必要がある。
func TestNewIDRejectsZeroAndNegative(t *testing.T) {
	for _, v := range []int64{0, -1, -9223372036854775808} {
		if _, err := org.NewID(v); !errors.Is(err, org.ErrInvalid) {
			t.Fatalf("NewID(%d) がエラーを返さなかった", v)
		}
	}
}

// TestZeroValueIsNotUsableAsAnID は、Go の型システムで塞げない穴を明示する。
//
// 🔑 `var id org.ID` は必ず作れてしまう。言語では防げないので、
// 「ゼロ値は無効値」という契約をテストで宣言し、境界で検証する責任を残す。
// この性質を忘れて `if id == 0 { id = defaultOrg }` と書いた瞬間に ADR 0003 が破れる。
func TestZeroValueIsNotUsableAsAnID(t *testing.T) {
	var zero org.ID

	if zero.Int64() != 0 {
		t.Fatalf("ゼロ値の Int64() = %d, want 0", zero.Int64())
	}

	if _, err := org.NewID(zero.Int64()); err == nil {
		t.Fatal("ゼロ値から NewID が成功してしまった。無効値の定義が壊れている")
	}
}

// TestStringIsDecimal は、ログや URL に出す表記が10進であることを固定する。
func TestStringIsDecimal(t *testing.T) {
	id, err := org.NewID(42)
	if err != nil {
		t.Fatalf("NewID(42) が失敗した: %v", err)
	}

	if got, want := id.String(), "42"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
