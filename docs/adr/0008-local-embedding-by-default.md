# ADR 0008: 埋め込みはローカル実行（Ollama + bge-m3）を既定とする

## Status

accepted (2026-09-01) — **amends ADR 0005**（インタフェースの決定は有効。既定値のみ反転する）

## Context

ADR 0005 では埋め込みプロバイダをインタフェースで抽象化し、
**既定を Voyage AI（外部 API）**、ローカルは「口だけ用意して未実装」と決めた。

その後、施主から前提が示された（2026-09-01）:

> このRAG、ローカル利用が前提

さらに実機を確認した結果、ローカル実行の条件が揃っていた:

| 項目 | 実測値 |
| --- | --- |
| CPU | 20 コア |
| RAM | 31GB（空き 29GB） |
| GPU | **NVIDIA GeForce RTX 3090** |
| WSL GPU | `/dev/dxg` あり（カーネル側パススルーは有効）。ただし `nvidia-smi` と `/usr/lib/wsl/lib` が無い |

外部 API を使う理由が無い。むしろ ADR 0005 が挙げた
「自己ホストの建前（Keep everything on your stack）と外部 API 依存の衝突」が、
ローカル実行によって完全に解消される。

なお **Anthropic に埋め込み API は無い**という ADR 0005 の前提は変わらない。
また、施主が保有する ChatGPT / Grok の**サブスクリプションは API アクセス権ではない**。
仮に API を契約しても、生成モデルは埋め込みの代替にならない——
生成モデルに「このテキストのベクトルを出せ」と頼んでも、
それらしい数値を作文するだけで意味的な距離が保存されない。

## Decision

**既定を `RECALL_EMBEDDER=ollama` に反転する。Voyage は任意経路に降格する。**

- 埋め込みモデルの既定は **`bge-m3`**（BAAI）
  - ライセンス **MIT**（商用可）・**1024次元**・**8192トークン文脈長**・100言語以上
  - **1024次元は `voyage-4` の既定と一致する**ので、将来 Voyage に切り替えてもスキーマが変わらない
- Ollama は **Windows 側でネイティブ実行**し、Recall（WSL）から HTTP で叩く
  - WSL の CUDA ユーザ空間ライブラリが未配置（`/usr/lib/wsl/lib` が無い）ため、
    Windows ネイティブなら**そのセットアップを回避して 3090 をそのまま使える**
  - Recall 側は HTTP クライアントのみ。cgo を持ち込まない（ADR 0007 と整合）
- `config.Load()` の `VOYAGE_API_KEY` 必須チェックは、`RECALL_EMBEDDER=voyage` のときだけに限定する
- **ADR 0005 の `Embedder` インタフェースは変更しない。** 実装を足すだけで済む

### `Kind` の翻訳はプロバイダ実装の責務

`Embedder.Embed` が取る `Kind`（`document` / `query`）は、プロバイダごとに要求が異なる:

| プロバイダ | `Kind` の扱い |
| --- | --- |
| Voyage | `input_type` パラメータとして送る（省略は品質低下・公式 FAQ が明記） |
| `bge-m3` | 接頭辞・パラメータとも**不要**。無視してよい |
| `multilingual-e5` | **`query: ` / `passage: ` の接頭辞が必須**。付け忘れると品質が落ちる |

この差異を実装側に閉じ込めるのがインタフェースの役割である。
**呼び出し側は常に `Kind` を渡す**——実装が使うかどうかに関わらず。

## Consequences

**得るもの**

- **API 課金がゼロになる。** 完全オフラインで動く
- 埋め込み対象のデータが一切外部に出ない。自己ホストの建前と実装が一致する
- 200M 無料枠の残量を気にする必要が消える。
  ADR 0005 の罠（モデル切替で全ベクトル再生成）を踏んでも、費用面の損失が無くなる

**払うもの**

- Ollama プロセスへの依存が増える。**検索時もクエリの埋め込みが要る**ので、
  Ollama が落ちていると検索そのものが失敗する（保存済みベクトルは無事だが照合できない）
- WSL → Windows のホスト間 HTTP 呼び出しになるため、接続先アドレスの解決が要る
- GPU の実効スループットは**未実測**。10万チャンクの取り込みに何分かかるかは、
  ADR 0007 のベンチマークと併せて測る

**変わらないこと**

- **モデルを変えたら保存済みベクトルは無効**（ADR 0005 の罠）。
  ローカルでも同じで、むしろ切替が容易になるぶん踏みやすい。
  `Embedder.ID()`（例 `bge-m3:1024`）の記録による検知は、これまで以上に重要になる

## 参照

- [BAAI/bge-m3 (Hugging Face)](https://huggingface.co/BAAI/bge-m3) — MIT・1024次元・8192トークン
- [Embeddings (Anthropic)](https://platform.claude.com/docs/en/build-with-claude/embeddings) — Anthropic は埋め込みモデルを提供しない

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Amends: ADR 0005（既定値のみ。インタフェースの決定は有効）
- Superseded by: none
