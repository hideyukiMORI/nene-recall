# ADR 0005: 埋め込みプロバイダをインタフェースで抽象化する

## Status

accepted (2026-09-01)

## Context

**Anthropic は埋め込みモデルを提供していない。** 公式ドキュメントの明記:

> Anthropic does not offer its own embedding model.
> — platform.claude.com/docs/en/build-with-claude/embeddings （2026-09-01 参照）

公式が案内するのは Voyage AI で、`voyage-4` 系（32,000 文脈長・既定1024次元）が推奨されている。
一方 NeNe シリーズの製品としての建前は "Keep everything on your stack" ——
自己ホストで外部 SaaS に データを渡さないことが売りである。
埋め込みのためだけに外部 API 必須にすると、**この建前と正面から衝突する**。

ここで効くのが `voyage-4-nano` が **Apache 2.0 で Hugging Face に公開されている**こと。
開放重みが存在するので、将来ローカル推論に寄せる道が閉じていない。

## Decision

埋め込み生成を単一のインタフェースの背後に置く。

```go
type Embedder interface {
    // Embed は input_type を明示して埋め込みを生成する。
    Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)
    // Dimensions は生成されるベクトルの次元数を返す。
    Dimensions() int
    // ID は保存済みベクトルとの互換判定に使う識別子（例 "voyage-4:1024"）。
    ID() string
}
```

- **既定実装は Voyage AI**（`voyage-4`）。設定 `RECALL_EMBEDDER=voyage`
- ローカル実装の口を最初から開けておく。`RECALL_EMBEDDER=local`（Phase 1 では未実装で可）
- `input_type` は**必ず指定する**。取り込み時 `document`、検索時 `query`。
  公式 FAQ が「省略や `None` は検索品質を落とす」と明記しており、
  これは実装の任意事項ではなく**プロバイダの要求**として扱う
- `Embedder.ID()` を保存済みベクトルのメタデータに記録し、
  **モデルを切り替えたら既存ベクトルを無効として扱う**。次元が同じでもベクトル空間は互換でない

## Consequences

**得るもの**

- 外部 API 必須という設計にならず、自己ホストの建前を将来にわたって守れる
- プロバイダの値上げ・仕様変更・終了に対して、差し替え1箇所で対応できる

**払うもの**

- インタフェース1枚ぶんの間接化。Phase 1 では実装が1つしかないので、
  この抽象は**当面ペイしない**。将来の選択肢を買うためのコストとして受け入れる
- API キーの管理が要る。環境変数のみとし、ログ・エラー応答に出さない

**罠**

- **モデル切替時に既存ベクトルを再生成しないと、検索結果が静かに壊れる。**
  次元が一致していても異なるモデルのベクトルは比較できず、
  エラーにならないまま無意味なスコアが返る。`Embedder.ID()` の記録はこれを検知するためにある

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
