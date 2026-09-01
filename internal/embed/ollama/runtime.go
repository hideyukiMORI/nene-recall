package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// Runtime は Ollama のランタイム版と、使っているモデルの digest。
//
// 🔑 これは評価レポートの再現性のために要る
// (docs/adr/0013-evaluation-harness-design.md)。Embedder.ID() は
// "bge-m3:1024"（モデル名＋次元）でしかなく、digest もランタイム版も含まない。
// 同じタグで別の重みが引かれても ADR 0005 の不一致検知は発火しない
// (docs/benchmarks/2026-09-01-baseline.md の追記が残リスクとして記録している)。
// embedder_id の書式を変えるのは再取り込みを伴う別の判断なので、
// ここでは「記録に残して後から突き合わせられるようにする」ことで穴を塞ぐ。
type Runtime struct {
	// Version は /api/version が返すランタイムの版。例 "0.33.2"。
	Version string
	// Digest は /api/tags が返すモデルの digest（64桁の16進）。
	// 一覧に見つからなければ空になる。
	Digest string
}

// Runtime はランタイムの版とモデルの digest を取得する。
//
// 🔴 embed.Embedder の契約には足していない。ランタイムの素性は
// 「テキストをベクトルに変換する」という契約の一部ではなく、実装ごとに
// 意味が違う。Ping と同じ扱いである。
//
// 🔴 digest は /api/tags から取る。/api/show には digest が無い（2026-09-01・
// Ollama 0.33.2 で実測。details・model_info・tensors はあるが digest フィールドは
// 存在しない）。Ping が /api/show を使っているからといって、そちらから取ろうと
// しないこと。
func (c *Client) Runtime(ctx context.Context) (Runtime, error) {
	version, err := c.version(ctx)
	if err != nil {
		return Runtime{}, err
	}

	digest, err := c.modelDigest(ctx)
	if err != nil {
		return Runtime{}, err
	}

	return Runtime{Version: version, Digest: digest}, nil
}

// version は /api/version からランタイムの版を読む。
func (c *Client) version(ctx context.Context) (string, error) {
	body, status, err := c.get(ctx, "/api/version")
	if err != nil {
		return "", err
	}

	if status != http.StatusOK {
		return "", c.classifyStatus(status, body)
	}

	var parsed versionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("%w: decode version: %w", embed.ErrProviderUnavailable, err)
	}

	return parsed.Version, nil
}

// modelDigest は /api/tags から使用中モデルの digest を探す。
//
// ⚠️ 見つからなければ空文字を返す。error にしない。digest はレポートの
// 付帯情報であって、評価そのものを止める理由にならない。取得できなかったことは
// 「空である」という形でレポートに残る。
func (c *Client) modelDigest(ctx context.Context) (string, error) {
	body, status, err := c.get(ctx, "/api/tags")
	if err != nil {
		return "", err
	}

	if status != http.StatusOK {
		return "", c.classifyStatus(status, body)
	}

	var parsed tagsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("%w: decode tags: %w", embed.ErrProviderUnavailable, err)
	}

	want := qualifiedModel(c.model)
	for _, m := range parsed.Models {
		if m.Name == want || m.Model == want {
			return m.Digest, nil
		}
	}

	return "", nil
}

// qualifiedModel はタグを省略したモデル指定に :latest を補う。
//
// 🔴 これが無いと照合が必ず外れる。設定は RECALL_EMBED_MODEL=bge-m3 と書くが、
// /api/tags が返す name は "bge-m3:latest" である（実測）。タグを省略した
// 指定は :latest を意味する、という Ollama の規則をここで解く。
func qualifiedModel(model string) string {
	if strings.Contains(model, ":") {
		return model
	}

	return model + ":latest"
}
