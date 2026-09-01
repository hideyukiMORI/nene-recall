# ADR 0012: 埋め込みプロバイダの実装は契約パッケージ配下のサブパッケージに置く

## Status

accepted (2026-09-01)

## Context

Phase 1 の 2番目「Ollama 埋め込みクライアント」を書く場所が決まらない。

`CLAUDE.md` は `internal/embed/ollama.go` と書いていたが、**これは実現不能である。**
ARC-002（中核は純粋に保つ）は `internal/embed` を pure-core として扱い、
`.golangci.yml` の depguard が `net/http`・`time`・`os`・`database/sql`・`log`・`log/slog` を
締め出している。一方 Ollama クライアントは **HTTP クライアントそのもの**で、
`net/http` と（タイムアウトのため）`time` を必要とする。

実測した（本 ADR を書く前に確認した）:

```
internal/embed/probe_contract.go:4:2: import 'net/http' is not allowed from list 'pure-core' (depguard)
internal/embed/probe_contract.go:5:2: import 'time' is not allowed from list 'pure-core' (depguard)
```

同じ内容を `internal/embed/ollama/` に置くと **0 issues** になる。
理由は depguard の files glob が gobwas/glob（区切り `/`）でコンパイルされ、
`**/internal/embed/*.go` の `*` が `/` を跨がないためで、サブディレクトリは
pure-core の対象に入らない。

**この帰結を黙って利用するのは危険である。** 「glob の穴」に見えるものへ実装を逃がすと、
次に誰かが glob を `**/internal/embed/**/*.go` に「修正」した瞬間に破綻し、
しかもそのとき初めて設計意図が失われていたことが分かる。

先例がある。`internal/index`（契約・純粋）と `internal/store/postgres`（実装・`database/sql` 可）は
既にこの形で、`store-is-wired-only-in-cmd` が具体ストアへの依存を配線点に閉じている。
**埋め込みも同型にできる。**

## Decision

**埋め込みプロバイダの実装は、契約パッケージ配下のサブパッケージに置く。**
Ollama クライアントは `internal/embed/ollama`、将来の Voyage は `internal/embed/voyage`。
`internal/embed` 自体は契約（`Embedder`・`Kind`・sentinel）だけを持ち、純粋なまま保つ。

glob の帰結に依存する形を「たまたま通る」状態にしないため、**次の3点をセットで行う**。

1. 本 ADR で、置き場所の規則と理由を明文化する
2. `docs/coding-rules.md` の ARC-002 に**適用範囲**を書く。pure-core の対象は
   **契約パッケージ本体**であり、実装サブパッケージは対象外で、代わりに配線制限を受ける
3. depguard に `embedder-is-wired-only-in-cmd` を足す。**これは強化であって緩和ではない**
   ので QLT-005 の ADR 要件には当たらない（ゲートは1バイトも緩めない）

```yaml
embedder-is-wired-only-in-cmd:
  files:
    - "!**/cmd/**"
    - "!**/internal/embed/ollama/**"
  deny:
    - pkg: "github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
```

### 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **`pure-core` から `net/http` を外す** | ゲートの緩和であり QLT-005 の対象。しかも影響が局所で済まない: `internal/embed` は `httpapi` も `store` も import する契約パッケージなので、**それら全層が推移的に HTTP 実装をリンクできる**ようになる。加えて `time` も芋づるで要り、ARC-002 が守ろうとしていた決定性（時刻・通信を値として渡す）が契約層から消える |
| **`internal/adapter/ollama` という別ツリー** | 機械的には等価で、pure-core にも掛からない。しかし「埋め込みの実装がどこにあるか」がツリーから読めなくなる。`internal/store/postgres` が既に「契約の隣に実装」の形を作っている以上、埋め込みだけ別ツリーにすると規則が2つになる |
| **transport を注入し、HTTP の型を自前定義する** | 契約パッケージに `net/http` を入れずに済むが、`http.RoundTripper` の劣化再発明になる。抽象1枚のために標準ライブラリの型を写し取るのは、得るものより保守対象のほうが大きい |
| **`internal/embed` を pure-core から外す** | 契約が純粋でなくなる。`Embedder` は「テキスト→ベクトル」の意味だけを持つべきで、その意味に HTTP は含まれない |

## Consequences

**得るもの**

- 契約が純粋なまま保たれる。`internal/embed` を import する層は HTTP も時計も引き込まない
- `internal/index` ↔ `internal/store/postgres` と同型になり、規則が1つで済む
- 具体 Embedder への依存が depguard で配線点に閉じる。`httpapi` が
  `ollama.Client` を直接掴む経路が機械的に塞がれる

**払うもの**

- **「`internal/embed` は import してよいが `internal/embed/ollama` はだめ」という非対称**を
  覚える必要がある。ただし覚えるのは人ではなく depguard である
- pure-core の適用範囲が「パッケージ本体のみ」であることが、glob の書き方に依存している

**正直に記録しておくこと**

- 🔴 **depguard の files glob を更新するときは、`*` が `/` を跨がないことを再確認すること。**
  `**/internal/embed/*.go` を `**/internal/embed/**` に「揃える」変更は、
  本 ADR の前提を壊す。壊すなら壊すで構わないが、そのときは実装の置き場所を
  同時に決め直す必要がある。片方だけ動かさない
- 本 ADR は「実装をどこに置くか」の決定であって、pure-core を緩める決定ではない。
  ARC-002 の効力は契約パッケージ本体において一切変わっていない

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
- 関連: ADR 0005（Embedder をインタフェースで抽象化する）、
  ADR 0008（既定をローカル実行にする）、
  ADR 0010（機械強制。本 ADR の3点セットはこの ADR の思想の適用）
