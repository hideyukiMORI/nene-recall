package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// chunkInput は投入する1件。OpenAPI の ChunkInput と対応する。
//
// ChunkID をポインタで受けるのは、指定されたことを検知して拒否するためである。
// 値型にすると 0（未指定）と 0 を明示した場合が区別できない
// (internal/httpapi/chunks.go と同じ理由)。
type chunkInput struct {
	ChunkID *int64 `json:"chunk_id,omitempty"`
	// ExternalID は呼び出し側（Corpus）の id。JSONL に書かれていればそのまま送る。
	//
	// 🔴 CLI が値を作らない。0 を補ったり chunk_id から写したりしない
	// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1)。
	ExternalID   *int64  `json:"external_id,omitempty"`
	DocumentID   int64   `json:"document_id"`
	SourceID     int64   `json:"source_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Content      string  `json:"content"`
	PageNumber   *int    `json:"page_number,omitempty"`
	SectionLabel *string `json:"section_label,omitempty"`
}

// putChunksRequest は POST /v1/chunks の本文。
type putChunksRequest struct {
	OrgID  int64        `json:"org_id"`
	Chunks []chunkInput `json:"chunks"`
}

// putChunksResponse は POST /v1/chunks の応答。
type putChunksResponse struct {
	Accepted    int      `json:"accepted"`
	ChunkIDs    []int64  `json:"chunk_ids"`
	ExternalIDs []*int64 `json:"external_ids"`
}

// deleteCountResponse は一括削除の応答。source 単位と document 単位で共通。
type deleteCountResponse struct {
	Deleted int `json:"deleted"`
}

// JSONL の1行の大きさ。bufio.Scanner の既定 (64KiB) では日本語の長いチャンクが
// 途中で切れ、その行だけが JSON として壊れるという分かりにくい失敗になる。
const (
	initialLineBuffer = 64 * 1024
	maxLineBytes      = 4 * 1024 * 1024
)

// cmdPut は JSONL を読んで POST /v1/chunks を叩く。
//
// 🔴 全行を1リクエストにまとめる。分割送信はしない——途中で失敗すると
// 「一部だけ入った」状態が残り、どこまで入ったかを利用者が知る手段が無い。
// 大きすぎる入力はサーバの WriteTimeout（60秒）で切れるので、そのときは
// 入力側で分けること。
func cmdPut(ctx context.Context, args []string, s streams) error {
	fs := newFlagSet("put", s.err)
	common := registerCommon(fs)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("put: %w", err)
	}

	if fs.NArg() > 1 {
		return fmt.Errorf("%w: put takes at most one file", errUsage)
	}

	opts, err := common.resolve()
	if err != nil {
		return err
	}

	announceOrg(s.err, opts)

	chunks, err := readChunks(fs.Arg(0), s.in)
	if err != nil {
		return err
	}

	return sendPut(ctx, opts, chunks, s)
}

// readChunks は JSONL を読み込む。path が空なら標準入力から読む。
func readChunks(path string, stdin io.Reader) ([]chunkInput, error) {
	if path == "" {
		return parseChunks(stdin)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %w", errUsage, path, err)
	}

	chunks, parseErr := parseChunks(file)
	if err := errors.Join(parseErr, file.Close()); err != nil {
		return nil, err
	}

	return chunks, nil
}

// parseChunks は1行1件の JSONL を読む。空行は無視する。
//
// 🔴 chunk_id が入っていたら送る前に落とす。サーバも 400
// (chunk_id_not_accepted) を返すが、CLI 側で止めるのは、100行のうち 1行だけに
// 混ざっていた場合に「何行目か」を利用者へ返せるのがここだけだからである。
// 外部システムの id は external_id で渡す
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1)。
func parseChunks(r io.Reader) ([]chunkInput, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, initialLineBuffer), maxLineBytes)

	var chunks []chunkInput

	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		parsed, err := parseChunkLine(line, text)
		if err != nil {
			return nil, err
		}

		chunks = append(chunks, parsed)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read input: %w", errUsage, err)
	}

	if len(chunks) == 0 {
		return nil, fmt.Errorf("%w: no chunks on stdin or in the given file", errUsage)
	}

	return chunks, nil
}

// parseChunkLine は1行を chunkInput にする。
func parseChunkLine(line int, text string) (chunkInput, error) {
	var in chunkInput
	if err := json.Unmarshal([]byte(text), &in); err != nil {
		return chunkInput{}, fmt.Errorf("%w: line %d is not valid JSON: %w", errUsage, line, err)
	}

	if in.ChunkID != nil {
		return chunkInput{}, fmt.Errorf(
			"%w: line %d sets chunk_id; use external_id for your own id "+
				"(Recall assigns chunk_id itself)", errUsage, line)
	}

	// 🔴 0 と負値をここで落とす。「外部 id を持たない」はキーの省略で表す。
	// サーバも 400 を返すが、行番号を言えるのは CLI だけである。
	if in.ExternalID != nil && *in.ExternalID < 1 {
		return chunkInput{}, fmt.Errorf(
			"%w: line %d has external_id %d; it must be a positive integer "+
				"(omit the key when there is no external id)", errUsage, line, *in.ExternalID)
	}

	if in.Content == "" {
		return chunkInput{}, fmt.Errorf("%w: line %d has an empty content", errUsage, line)
	}

	return in, nil
}

// sendPut は投入要求を送って結果を書く。
func sendPut(ctx context.Context, opts options, chunks []chunkInput, s streams) error {
	body, err := json.Marshal(putChunksRequest{OrgID: opts.orgID.Int64(), Chunks: chunks})
	if err != nil {
		return fmt.Errorf("%w: encode put request: %w", errUsage, err)
	}

	raw, err := newClient(opts).do(ctx, request{
		method:         http.MethodPost,
		path:           "/v1/chunks",
		body:           body,
		tolerateStatus: 0,
	})
	if err != nil {
		return err
	}

	if opts.asJSON {
		return writeRaw(s.out, raw)
	}

	var resp putChunksResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("recallctl: decode put response: %w", err)
	}

	pretty, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return fmt.Errorf("recallctl: encode put result: %w", err)
	}

	return writeRaw(s.out, pretty)
}

// cmdDelete は DELETE /v1/chunks/{chunk_id} を叩く。
//
// org_id はクエリ文字列で渡す。本文ではない——OpenAPI の deleteChunk が
// `org_id` を in: query の必須パラメータとして定めており、
// internal/httpapi の requireOrgID も r.URL.Query() から読む。
func cmdDelete(ctx context.Context, args []string, s streams) error {
	opts, id, err := parseIDCommand("delete", args, s)
	if err != nil {
		return err
	}

	_, err = newClient(opts).do(ctx, request{
		method:         http.MethodDelete,
		path:           "/v1/chunks/" + strconv.FormatInt(id, 10) + orgQuery(opts),
		body:           nil,
		tolerateStatus: 0,
	})

	return err
}

// cmdDeleteSource は DELETE /v1/sources/{source_id}/chunks を叩く。
func cmdDeleteSource(ctx context.Context, args []string, s streams) error {
	opts, id, err := parseIDCommand("delete-source", args, s)
	if err != nil {
		return err
	}

	raw, err := newClient(opts).do(ctx, request{
		method:         http.MethodDelete,
		path:           "/v1/sources/" + strconv.FormatInt(id, 10) + "/chunks" + orgQuery(opts),
		body:           nil,
		tolerateStatus: 0,
	})
	if err != nil {
		return err
	}

	return renderDeleteCount(opts, raw, s)
}

// cmdDeleteDocument は DELETE /v1/documents/{document_id}/chunks を叩く。
//
// source 単位と別に要るのは、Corpus の削除経路が document 単位と source 単位の
// 2つだからである (docs/adr/0020-phase2-corpus-integration-contract.md Decision 2)。
func cmdDeleteDocument(ctx context.Context, args []string, s streams) error {
	opts, id, err := parseIDCommand("delete-document", args, s)
	if err != nil {
		return err
	}

	raw, err := newClient(opts).do(ctx, request{
		method:         http.MethodDelete,
		path:           "/v1/documents/" + strconv.FormatInt(id, 10) + "/chunks" + orgQuery(opts),
		body:           nil,
		tolerateStatus: 0,
	})
	if err != nil {
		return err
	}

	return renderDeleteCount(opts, raw, s)
}

// renderDeleteCount は一括削除の応答を書く。
//
// source 単位と document 単位で同じ形にしてある。片方だけ表示が違うと、
// 呼び出し側（スクリプト）が2つを同じように扱えない。
func renderDeleteCount(opts options, raw []byte, s streams) error {
	if opts.asJSON {
		return writeRaw(s.out, raw)
	}

	var resp deleteCountResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("recallctl: decode delete response: %w", err)
	}

	out := newTextWriter(s.out)
	out.printf("deleted=%d\n", resp.Deleted)

	return out.Err()
}

// parseIDCommand は「id を1つ取る削除系コマンド」の共通処理をまとめる。
func parseIDCommand(name string, args []string, s streams) (options, int64, error) {
	fs := newFlagSet(name, s.err)
	common := registerCommon(fs)

	if err := fs.Parse(args); err != nil {
		return options{}, 0, fmt.Errorf("%s: %w", name, err)
	}

	if fs.NArg() != 1 {
		return options{}, 0, fmt.Errorf("%w: %s takes exactly one id", errUsage, name)
	}

	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return options{}, 0, fmt.Errorf("%w: %s: id must be an integer: %w", errUsage, name, err)
	}

	opts, err := common.resolve()
	if err != nil {
		return options{}, 0, err
	}

	announceOrg(s.err, opts)

	return opts, id, nil
}

// orgQuery は org_id のクエリ文字列を作る。
func orgQuery(o options) string { return "?org_id=" + o.orgID.String() }
