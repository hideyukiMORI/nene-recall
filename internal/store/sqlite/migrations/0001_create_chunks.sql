-- 0001: chunks テーブルと FTS5 の外部コンテンツ表。
--
-- 🔴 マイグレーションは forward-only である。down を書かない。
--    詳細と理由は migrate.go の冒頭コメントを参照。
--
-- 🔑 postgres 側は 0001（本体）と 0002（語彙）の2段に分かれているが、こちらは
--    1本にしてある。あちらの2段は「既に本番データが入っている表に語彙列を
--    後から足した」という履歴そのもので、履歴の無いストアに写す意味が無い。
--    列の並びと CHECK の内容は postgres 側の 0001 + 0002 と同じである。

CREATE TABLE chunks (
    -- 🔴 AUTOINCREMENT を外さないこと (ADR 0017 Decision 5)。
    --
    -- 素の INTEGER PRIMARY KEY は rowid の別名であり、SQLite は「現在の最大値
    -- + 1」を割り当てる。つまり最大 id の行を消すと、次の挿入がその id を
    -- 再び使う。評価ハーネスは eval_key → 採番 id の写像を Put の戻り値から
    -- 実行時に作る (ADR 0013) ので、id が再利用されると、注釈が静かに別の行を
    -- 指す。AUTOINCREMENT は sqlite_sequence に最大値を記録し、再利用を止める。
    id            INTEGER PRIMARY KEY AUTOINCREMENT,

    org_id        INTEGER NOT NULL CHECK (org_id >= 1),
    document_id   INTEGER NOT NULL,
    source_id     INTEGER NOT NULL,
    chunk_index   INTEGER NOT NULL CHECK (chunk_index >= 0),
    content       TEXT    NOT NULL CHECK (content <> ''),
    page_number   INTEGER,
    section_label TEXT,

    -- embedder_id / tokenizer_id を単一行のメタ表ではなく行ごとの列に持つ。
    --
    -- メタ表にすると、全行を削除してからモデルや分割器を切り替えたときに
    -- 古いメタだけが残り、空のストアが新規の取り込みを誤って拒否する。
    -- 行ごとに持てば「行が無ければ矛盾も無い」となり、その状態は自己治癒する。
    -- 不一致の検知は「自分と違う値の行が1件でもあるか」で行う。
    embedder_id   TEXT    NOT NULL CHECK (embedder_id <> ''),
    tokenizer_id  TEXT    NOT NULL CHECK (tokenizer_id <> ''),

    -- float32 × 1024 をリトルエンディアンで並べた 4096 バイト。
    -- Go 側の vectorBytes と対になる。長さの CHECK は「Go を通さない INSERT」に
    -- 対する最初の防壁で、読み取り時の decodeVector が二枚目の防壁である。
    embedding     BLOB    NOT NULL CHECK (length(embedding) = 4096),

    -- Tokenizer の出力を空白区切りで並べたもの。FTS5 の外部コンテンツ表が読む。
    --
    -- 空文字を許す。絵文字だけのチャンクのように、分割できる語が1つも無い本文は
    -- 正常な入力である（content 側の CHECK (content <> '') とは別の話）。
    lexeme_text   TEXT    NOT NULL,

    -- API には出さない。取り込み時期の追跡と、将来の再取り込み判断のために持つ。
    -- 既定値を DB 側に置くので Go は時刻を読まない。
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- 分離条件（org_id）と絞り込み条件を先頭に置いた B-tree 索引。
-- org_id 単独の索引を別に作らないのは、この複合索引の先頭列で足りるため。
CREATE INDEX chunks_org_document_idx ON chunks (org_id, document_id);
CREATE INDEX chunks_org_source_idx   ON chunks (org_id, source_id);

-- embedder_id / tokenizer_id 不一致の検知（自分と違う値の行を1件探す）に使う。
CREATE INDEX chunks_embedder_idx  ON chunks (embedder_id);
CREATE INDEX chunks_tokenizer_idx ON chunks (tokenizer_id);

-- 🔴🔴 embedding 列にベクトル索引を作らないこと。
--
-- SQLite にはそもそもベクトル索引が無いので「作れない」が、この禁止は
-- 拡張（sqlite-vec 等）を足すことも含めて掛かる。ADR 0007 の成果物は
-- 「pgvector を選んだこと」ではなく「測ってから索引を入れた経路」であり、
-- 比較対象は Go 側の総当たりそのものである (ADR 0017 Decision 2)。
-- 上の B-tree 索引はこの禁止の対象ではない。

-- chunks_fts は lexeme_text の転置索引。
--
-- 🔑 content='chunks' / content_rowid='id' の外部コンテンツ表にしてあるのは、
--    本文をもう一度持たないためである。FTS 側は転置索引だけを持ち、実体は
--    chunks が唯一の正になる。二重に持つと「片方だけ更新された行」が作れる。
--
-- 🔑 tokenize='ascii' を選ぶ理由 (ADR 0017 Decision 3)。ascii は非 ASCII を
--    1文字も分割しないので、Go 側 (internal/lexical/bigram) が切った CJK の
--    bigram トークンが FTS5 側で再分割される経路が塞がれる。unicode61 は
--    Unicode 分類で割るので、この保証が無い。
--
-- ⚠️ ascii は ASCII の記号を区切りとして扱う。実測: "recall_store" は
--    recall と store の2トークンになる。これは postgres の 'simple' パーサが
--    下線で割るのと同じ挙動であり、検索式も同じトークナイザを通る（フレーズ
--    検索になる）ので、両側で噛み合う。片側だけ囲みを外さないこと。
CREATE VIRTUAL TABLE chunks_fts USING fts5(
    lexeme_text,
    content='chunks',
    content_rowid='id',
    tokenize='ascii'
);

-- 🔴🔴 以下の3本の trigger を1本でも欠かすと、「エラーにならないまま語彙
--       スコアが常に 0」あるいは「消したはずの行が語彙検索に当たり続ける」に
--       なる。外部コンテンツ表は自分では同期しない。同期はここだけが担う。
--
-- delete の書き方（'delete' コマンドを INSERT する）は FTS5 の規定である。
-- 外部コンテンツ表は本文を保持しないので、消すときに古い値を渡して転置索引の
-- 差分を打ち消させる。old.lexeme_text を渡し損ねると索引が壊れる。
CREATE TRIGGER chunks_fts_after_insert AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts (rowid, lexeme_text) VALUES (new.id, new.lexeme_text);
END;

CREATE TRIGGER chunks_fts_after_delete AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, lexeme_text)
    VALUES ('delete', old.id, old.lexeme_text);
END;

-- update は「古い値を打ち消してから新しい値を入れる」の2手順である。
-- 現在の Writer は更新を行わない（insert-only・再取り込みは DeleteBySource →
-- Put の2手順）が、trigger を欠かすと将来 UPDATE を足した人が静かに索引を壊す。
CREATE TRIGGER chunks_fts_after_update AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, lexeme_text)
    VALUES ('delete', old.id, old.lexeme_text);
    INSERT INTO chunks_fts (rowid, lexeme_text) VALUES (new.id, new.lexeme_text);
END;
