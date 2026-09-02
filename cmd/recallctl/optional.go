package main

import (
	"fmt"
	"strconv"
	"strings"
)

// 🔴 このファイルの3つの型が解いている問題は1つである——
// 「指定しなかった」と「既定値と同じ値を指定した」の区別。
//
// limit も alpha も、未指定ならキーごと送らずサーバの既定に任せるのが契約で
// (docs/openapi/openapi.yaml)、かつ alpha=0 は「純語彙」という意味のある指定
// である（要件定義 Q-3・Makefile の EVAL_ALPHA のコメントも同じ罠を書いている）。
// ゼロ値で受けるとこの2つが同じ 0 になり、未指定のつもりが純語彙検索になる。

// optionalInt は「指定されたかどうか」を覚える int のフラグ。
type optionalInt struct {
	value int
	set   bool
}

// String は flag.Value を満たす。
func (o *optionalInt) String() string {
	if !o.set {
		return ""
	}

	return strconv.Itoa(o.value)
}

// Set は flag.Value を満たす。
func (o *optionalInt) Set(raw string) error {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	o.value = v
	o.set = true

	return nil
}

// ptr は指定されていれば値へのポインタを、されていなければ nil を返す。
func (o *optionalInt) ptr() *int {
	if !o.set {
		return nil
	}

	v := o.value

	return &v
}

// optionalFloat は「指定されたかどうか」を覚える float64 のフラグ。
type optionalFloat struct {
	value float64
	set   bool
}

// String は flag.Value を満たす。
func (o *optionalFloat) String() string {
	if !o.set {
		return ""
	}

	return strconv.FormatFloat(o.value, 'g', -1, 64)
}

// Set は flag.Value を満たす。
func (o *optionalFloat) Set(raw string) error {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	o.value = v
	o.set = true

	return nil
}

// ptr は指定されていれば値へのポインタを、されていなければ nil を返す。
func (o *optionalFloat) ptr() *float64 {
	if !o.set {
		return nil
	}

	v := o.value

	return &v
}

// int64List は繰り返し指定できる int64 のフラグ。
//
// -document 1 -document 2 のように複数回指定すると積み上がる。
type int64List []int64

// String は flag.Value を満たす。
func (l *int64List) String() string {
	parts := make([]string, 0, len(*l))
	for _, v := range *l {
		parts = append(parts, strconv.FormatInt(v, 10))
	}

	return strings.Join(parts, ",")
}

// Set は flag.Value を満たす。
func (l *int64List) Set(raw string) error {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}

	*l = append(*l, v)

	return nil
}
