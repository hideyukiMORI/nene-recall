// Package index は検索の契約を定める。
package index

import (
	"context"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// Query は1回の検索要求。
//
// OrgID は必須である。ゼロ値を「全 org」と解釈してはならない
// (docs/adr/0003-org-id-is-mandatory.md)。org.ID のゼロ値は無効値であり、
// この構造体を作る側が org.ParseID / org.NewID を通していることを前提にする。
type Query struct {
	OrgID       org.ID
	Text        string
	Limit       int
	Alpha       float32 // 合成の重み: alpha*vector + (1-alpha)*lexical
	DocumentIDs []int64
	SourceIDs   []int64
}

// Result は検索結果1件。
//
// VectorScore と LexicalScore を分けて保持するのは、検索が外したときに
// ベクトル側と語彙側のどちらが原因かを切り分けるため。合成値だけでは
// Alpha の調整が当てずっぽうになる。
type Result struct {
	Chunk        chunk.Chunk
	Score        float32
	VectorScore  float32
	LexicalScore float32
}

// Searcher はチャンクを検索する。
type Searcher interface {
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Writer はチャンクを投入・削除する。
//
// 削除系が orgID を引数に取るのは、呼び出し側の渡し忘れをコンパイルエラーに
// するため。分離条件をシグネチャに現す (ADR 0003)。
//
// 🔴 Put は「採番された id を入力と同じ順で返す」。評価ハーネスの
// eval_key → id の写像がこの契約から作られる (ADR 0013)。
// Chunk.ExternalID が非 nil の行は (org_id, external_id) で置き換えになるので、
// そのときに返るのは新しい採番ではなく既存行の id である
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1)。
// どちらの場合も「入力と同じ順」は変わらない。
type Writer interface {
	Put(ctx context.Context, orgID org.ID, chunks []chunk.Chunk) ([]int64, error)
	Delete(ctx context.Context, orgID org.ID, chunkID int64) error
	DeleteBySource(ctx context.Context, orgID org.ID, sourceID int64) (int, error)
	// DeleteByDocument は文書単位でまとめて削除し、消した件数を返す。
	//
	// source 単位と別に要るのは、Corpus の削除経路が document 単位と
	// source 単位の2つだからである (ADR 0020 Decision 2)。片方しか無いと、
	// Corpus 側で消した文書が Recall に残り、検索に出続ける。
	DeleteByDocument(ctx context.Context, orgID org.ID, documentID int64) (int, error)
}
