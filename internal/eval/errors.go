package eval

import "errors"

// ErrInvalidDataset は評価セットが読めない、または内部で矛盾していることを表す。
//
// 呼び出し側は errors.Is で判定する。メッセージ文字列で分岐しないこと (GO-005)。
// 「注釈の参照先が壊れている」は評価の前提が崩れている状態なので、
// 黙って読み飛ばさず必ずここに集める。
var ErrInvalidDataset = errors.New("eval: dataset is invalid")

// ErrMeasure は計測そのものが失敗したことを表す。
//
// 🔴 「検索が外した」は失敗ではない。それは測るべき結果であって error ではない。
// ここに来るのは投入・埋め込み・検索が実行できなかったときだけである。
var ErrMeasure = errors.New("eval: measurement failed")

// ErrRankingDiverged は、同じクエリに対して2系統の検索が違う順位を返したことを表す。
//
// 🔴 p95 は「埋め込み往復を含む／除く」の2系統で測る (ADR 0009) が、
// 両者が別のものを測っていたら比較に意味が無い。系統1 (Search) は自分でクエリを
// 埋め込み、系統2 (SearchVector) は事前に埋め込んだベクトルを受け取るので、
// 順位が食い違うということは次のどちらかが起きている:
//
//   - 埋め込みが非決定的である（同じ入力から違うベクトルが出た）
//   - スコアが同値の行があり、ORDER BY の並びが実行ごとに変わった
//
// どちらも「2系統の latency を並べて語れない」状態なので、静かに続けない。
var ErrRankingDiverged = errors.New("eval: the two search paths returned different rankings")

// ErrMissingDependency は計測に要る依存が注入されていないことを表す。
//
// ゼロ値の Dependencies で走らせると nil のインタフェースを呼ぶことになる。
// 配線の失敗は計測の途中ではなく開始前に落とす (GO-003)。
var ErrMissingDependency = errors.New("eval: a required dependency is missing")
