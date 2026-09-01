-- 0002: 語彙検索のためのトークン列と、分割器の識別子。
--
-- 🔴 マイグレーションは forward-only である。down を書かない。
--    詳細と理由は migrate.go の冒頭コメントを参照。

-- lexeme_text は Tokenizer が分割したトークンを空白区切りで並べたもの。
--
-- 🔴 本文（content）から DB 側で作らない。日本語の分割規則は Go 側の
--    Tokenizer が持っており、to_tsvector('simple', content) では
--    「検索対象」が丸ごと1レキシームになって「対象」で引けない。
--    分割は取り込み時に Go が行い、その結果をここに保存する。
--
-- 空文字を許す。絵文字だけのチャンクのように、分割できる語が1つも無い本文は
-- 正常な入力である（content 側の CHECK (content <> '') とは別の話）。
-- CHECK を置くとその入力が取り込めなくなる。
ALTER TABLE chunks
    ADD COLUMN lexeme_text TEXT NOT NULL DEFAULT '';

-- tokenizer_id は embedder_id と同じ流儀で行ごとに持つ。
--
-- 🔴 単一行のメタテーブルにしない。全行を削除してから分割器を切り替えたときに
--    古いメタだけが残り、空のストアが新規の取り込みを誤って拒否する。
--    行ごとに持てば「行が無ければ矛盾も無い」となり、その状態は自己治癒する。
--    不一致の検知は「自分と違う tokenizer_id の行が1件でもあるか」で行う。
--
-- 🔴 既定値 'unknown:before-0002' は、このマイグレーション以前に入っていた行に
--    付く印である。どの Tokenizer.ID() とも一致しないので、既存行が残ったまま
--    検索するとエラーになる。これは意図した挙動である（施主裁定 2026-09-02）:
--    静かに lexical_score = 0 で動き続けるより、エラーで止まって取り込み直しを
--    要求するほうが ADR 0005 の思想に合う。分割規則が違うトークン列を混ぜても
--    症状は「語彙スコアが少し低い」だけで、単一の分割器で開発している限り
--    一切表面化しない。
--
-- DEFAULT を直後に落とすのは、新規の投入で Go 側が値を渡し忘れたときに
-- 既定値で通ってしまう経路を残さないため。
ALTER TABLE chunks
    ADD COLUMN tokenizer_id TEXT NOT NULL DEFAULT 'unknown:before-0002';

ALTER TABLE chunks
    ALTER COLUMN lexeme_text DROP DEFAULT,
    ALTER COLUMN tokenizer_id DROP DEFAULT;

ALTER TABLE chunks
    ADD CONSTRAINT chunks_tokenizer_id_not_empty CHECK (tokenizer_id <> '');

-- lexemes は lexeme_text から DB が導出する生成列。
--
-- 🔑 生成列にするのは、Go 側の文字列と DB 側の tsvector がずれる経路を
--    構造的に無くすためである。アプリケーションが両方を書く形にすると、
--    片方だけ更新された行が「取り込みと検索で同じ関数を通しているのに
--    DB 内では別のレキシーム」という検出しにくい壊れ方をする。
--
-- 'simple' は語幹化も stop word 除去もしない設定。英語の語幹化を効かせると
-- 日本語の bigram には無意味な変換が入り、かつ「stop word だから消えた」という
-- 説明のつかない欠落が生まれる。
--
-- ⚠️ 'simple' パーサは記号で再分割し小文字化する。実測した挙動:
--      RECALL_STORE                 -> 'recall':1 'store':2  （下線で割れる）
--      pgvector 0.8.6               -> 'pgvector':1 '0.8.6':2（版番号は保つ）
--      PdoChunkSearchRepository.php -> 1レキシームのまま
--    割れること自体は問題にならない。検索側も to_tsquery で同じパーサを通すので
--    'recall' <-> 'store' という隣接クエリになり、両側で一致するためである。
--    🔴 ただし片側だけを引用符で囲む等でパーサを迂回すると、この一致が静かに
--    崩れる。往復同一性テスト（writer_test.go）がここを縛っている。
ALTER TABLE chunks
    ADD COLUMN lexemes tsvector
        GENERATED ALWAYS AS (to_tsvector('simple', lexeme_text)) STORED;

-- tokenizer_id 不一致の検知（自分と違う値の行を1件探す）に使う。
-- embedder_id の chunks_embedder_idx と同じ用途である。
CREATE INDEX chunks_tokenizer_idx ON chunks (tokenizer_id);

-- 🔴🔴 lexemes 列に GIN / GiST 索引を作らないこと。
--
-- ベクトル索引を最初から作らないのと同じ理由である
-- (docs/adr/0007-pgvector-over-brute-force.md)。索引の価値は
-- 「入れる前と後を測って比べた」ことにあり、最初から張ると before が取れない。
--
-- 加えて現在の検索は全行に ts_rank を計算する（@@ で絞り込まない）ので、
-- そもそも転置索引が使われる形になっていない。索引を検討するのは
-- 合成の形を先に固めてからで、それは Phase 1 項目7 の仕事である。
